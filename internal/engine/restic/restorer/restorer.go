// Package restorer walks a snapshot tree and writes its files into a
// staging directory, then moves the staged tree into the target. The
// repository is only ever read; nothing is written to it, and the target
// stays untouched on every abort path (staging + rename).
package restorer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/bqckup/bqckup-go/internal/engine/restic/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
	"golang.org/x/sys/unix"
)

// Summary reports what one restore produced.
type Summary struct {
	FilesRestored int
	BytesRestored int64
	SkippedPaths  []string
}

// Overwrite is called once, before anything is written to the target,
// with every existing path the restore would replace. A nil return means
// proceed; a non-nil error aborts the restore and is propagated unchanged.
type Overwrite func(conflicts []string) error

// Restorer restores snapshot trees through an opened repository.
type Restorer struct {
	repo *repository.Repository
}

func New(repo *repository.Repository) *Restorer { return &Restorer{repo: repo} }

type restoreState struct {
	paths   []string        // cleaned configured include paths
	matched map[string]bool // configured path -> some node matched it
	files   int
	bytes   int64
}

// Restore walks the snapshot tree, writes every node under one of the
// configured paths into a staging directory next to the target, collects
// conflicts, asks the consumer once, and moves the staged tree into place.
// On any error before the final move the staging directory is removed and
// the target is untouched.
func (r *Restorer) Restore(ctx context.Context, snap snapshot.Snapshot, paths []string, target string, confirm Overwrite) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if snap.Tree == nil {
		return Summary{}, fmt.Errorf("restorer: snapshot has no tree")
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return Summary{}, fmt.Errorf("restorer: resolve target: %w", err)
	}
	cleaned := make([]string, len(paths))
	for i, p := range paths {
		cleaned[i] = filepath.Clean(p)
	}
	// The staging directory must share a filesystem with the target, and
	// the target's parent may not exist yet.
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		return Summary{}, fmt.Errorf("restorer: create target parent: %w", err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(targetAbs), ".bqckup-restore-*")
	if err != nil {
		return Summary{}, fmt.Errorf("restorer: create staging directory: %w", err)
	}
	defer os.RemoveAll(staging) // no-op once the staged tree has been moved

	rootTree, err := r.repo.LoadTree(ctx, *snap.Tree)
	if err != nil {
		return Summary{}, err
	}
	state := &restoreState{paths: cleaned, matched: make(map[string]bool, len(cleaned))}
	for _, p := range cleaned {
		state.matched[p] = false
	}
	for _, node := range rootTree.Nodes {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		// Anchor each root node at the snapshot path with a matching
		// basename (the archiver names root nodes after their path base;
		// synthetic multi-root names like "data-1" match nothing and are
		// skipped, like restic).
		anchor := ""
		for _, p := range snap.Paths {
			if filepath.Base(p) == node.Name {
				anchor = filepath.Clean(p)
				break
			}
		}
		if anchor == "" {
			continue
		}
		// Multi-path snapshots wrap every root in one extra tree level: a
		// synthetic dir node whose subtree holds the single real root node
		// (same name, real metadata). Fold the wrapper away.
		effective := node
		if len(snap.Paths) > 1 && node.Subtree != nil {
			subtree, err := r.repo.LoadTree(ctx, *node.Subtree)
			if err != nil {
				return Summary{}, err
			}
			if len(subtree.Nodes) == 1 && subtree.Nodes[0].Name == node.Name {
				effective = subtree.Nodes[0]
			}
		}
		if err := r.restoreNode(ctx, state, staging, anchor, effective); err != nil {
			return Summary{}, err
		}
	}

	skipped := make([]string, 0)
	for p, matched := range state.matched {
		if !matched {
			skipped = append(skipped, p)
		}
	}
	sort.Strings(skipped)
	if len(paths) > 0 && len(skipped) == len(paths) {
		return Summary{}, fmt.Errorf("restorer: the snapshot contains none of the configured paths")
	}

	targetFD, targetExists, err := openRestoreTarget(targetAbs)
	if err != nil {
		return Summary{}, err
	}
	if targetFD >= 0 {
		defer unix.Close(targetFD)
	}
	var conflicts []string
	if targetExists {
		conflicts, err = collectMergeConflicts(staging, targetAbs, targetFD)
		if err != nil {
			return Summary{}, err
		}
	}
	if len(conflicts) > 0 && confirm != nil {
		if err := confirm(conflicts); err != nil {
			return Summary{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if err := r.moveIntoPlace(staging, targetAbs, targetFD, targetExists); err != nil {
		return Summary{}, err
	}
	return Summary{FilesRestored: state.files, BytesRestored: state.bytes, SkippedPaths: skipped}, nil
}

// restoreNode writes one node (when it belongs to a configured path) and
// recurses into its subtree. The absolute snapshot path of the node is
// passed down from the anchored root; the target layout mirrors it
// (restic layout: /var/www/html lands under <target>/var/www/html).
func (r *Restorer) restoreNode(ctx context.Context, st *restoreState, staging, absPath string, node *tree.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSnapshotNode(node); err != nil {
		return err
	}
	rel := filepath.FromSlash(strings.TrimLeft(absPath, "/"))
	stagingPath := filepath.Join(staging, rel)

	included := false
	for _, p := range st.paths {
		if absPath == p || strings.HasPrefix(absPath, p+"/") {
			included = true
			st.matched[p] = true
		}
	}
	if included {
		switch node.Type {
		case tree.TypeDir:
			// The synthetic multi-root wrapper nodes the archiver writes
			// carry no mode; fall back to a usable one and apply the real
			// mode below (MkdirAll never changes an existing directory's
			// mode, so the real node's Chmod wins over the wrapper's).
			perm := node.Mode.Perm()
			if perm == 0 {
				perm = 0o755
			}
			if err := os.MkdirAll(stagingPath, perm); err != nil {
				return fmt.Errorf("restorer: create directory %s: %w", stagingPath, err)
			}
			if node.Mode.Perm() != 0 {
				if err := os.Chmod(stagingPath, node.Mode.Perm()); err != nil {
					return fmt.Errorf("restorer: chmod directory %s: %w", stagingPath, err)
				}
			}
		case tree.TypeFile:
			if err := r.restoreFile(ctx, stagingPath, node); err != nil {
				return err
			}
			st.files++
			st.bytes += int64(node.Size)
		case tree.TypeSymlink:
			if err := os.Symlink(node.LinkTarget, stagingPath); err != nil {
				return fmt.Errorf("restorer: create symlink %s: %w", stagingPath, err)
			}
		default:
			// dev, fifo, socket: skipped in M18, like restic without root
		}
	}
	if node.Subtree != nil {
		subtree, err := r.repo.LoadTree(ctx, *node.Subtree)
		if err != nil {
			return err
		}
		for _, child := range subtree.Nodes {
			if err := r.restoreNode(ctx, st, staging, path.Join(absPath, child.Name), child); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSnapshotNode rejects structures that cannot originate from a
// filesystem tree. In particular, recursing beneath a staged symlink would
// follow its link target while the restore is still being assembled.
func validateSnapshotNode(node *tree.Node) error {
	if node == nil {
		return errors.New("restorer: snapshot tree contains a nil node")
	}
	if node.Name == "" || node.Name == "." || node.Name == ".." || strings.Contains(node.Name, "/") || strings.ContainsRune(node.Name, 0) {
		return fmt.Errorf("restorer: snapshot node has invalid name %q", node.Name)
	}
	if node.Subtree != nil && node.Type != tree.TypeDir {
		return fmt.Errorf("restorer: snapshot %s node %q cannot contain a subtree", node.Type, node.Name)
	}
	return nil
}

// restoreFile writes one regular file into staging: blobs, then the mode
// and modification time recorded in the node.
func (r *Restorer) restoreFile(ctx context.Context, stagingPath string, node *tree.Node) error {
	file, err := os.OpenFile(stagingPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, node.Mode.Perm())
	if err != nil {
		return fmt.Errorf("restorer: create file %s: %w", stagingPath, err)
	}
	writeErr := func() error {
		defer file.Close()
		for _, id := range node.Content {
			data, err := r.repo.LoadBlob(ctx, restic.DataBlob, id)
			if err != nil {
				return err
			}
			if _, err := file.Write(data); err != nil {
				return fmt.Errorf("restorer: write file %s: %w", stagingPath, err)
			}
		}
		return nil
	}()
	if writeErr != nil {
		return writeErr
	}
	if err := os.Chmod(stagingPath, node.Mode.Perm()); err != nil {
		return fmt.Errorf("restorer: chmod %s: %w", stagingPath, err)
	}
	if err := os.Chtimes(stagingPath, node.ModTime, node.ModTime); err != nil {
		return fmt.Errorf("restorer: set times on %s: %w", stagingPath, err)
	}
	return nil
}

// openRestoreTarget opens an existing target without following a final
// symlink. The returned directory descriptor anchors all merge operations to
// the directory that was securely opened.
func openRestoreTarget(target string) (fd int, exists bool, err error) {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return -1, false, nil
		}
		return -1, false, fmt.Errorf("restorer: inspect target %s: %w", target, err)
	}
	if !info.IsDir() {
		return -1, false, fmt.Errorf("restorer: target %s exists and is not a directory", target)
	}
	fd, err = unix.Open(target, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, false, fmt.Errorf("restorer: open target %s: %w", target, err)
	}
	return fd, true, nil
}

// collectMergeConflicts compares the staged tree with the opened target
// without following target symlinks. Walking staging also discovers synthetic
// path directories (for example, target/var for a configured /var/www path),
// which were not represented by snapshot nodes and therefore used to bypass
// conflict discovery.
func collectMergeConflicts(staging, target string, targetFD int) ([]string, error) {
	var conflicts []string
	err := filepath.WalkDir(staging, func(stagingPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(staging, stagingPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		exists, isDir, err := targetEntryAt(targetFD, rel)
		if err != nil {
			return err
		}
		if exists && (!entry.IsDir() || !isDir) {
			conflicts = append(conflicts, filepath.Join(target, rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("restorer: inspect target conflicts: %w", err)
	}
	return conflicts, nil
}

// targetEntryAt reports one target entry relative to targetFD. If an
// intermediate component does not exist or is not a real directory, the entry
// is unreachable and therefore does not add a duplicate descendant conflict.
func targetEntryAt(targetFD int, rel string) (exists, isDir bool, err error) {
	parentFD, reachable, err := openParentAt(targetFD, filepath.Dir(rel), false)
	if err != nil || !reachable {
		return false, false, err
	}
	defer unix.Close(parentFD)
	var stat unix.Stat_t
	err = unix.Fstatat(parentFD, filepath.Base(rel), &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, stat.Mode&unix.S_IFMT == unix.S_IFDIR, nil
}

// moveIntoPlace puts the staged tree at the target. A missing target is one
// rename. An existing target is merged through directory-relative operations,
// and no target symlink is followed during traversal.
func (r *Restorer) moveIntoPlace(staging, target string, targetFD int, targetExists bool) error {
	if !targetExists {
		if err := os.Rename(staging, target); err != nil {
			return fmt.Errorf("restorer: move staged restore into place: %w", err)
		}
		return nil
	}
	return filepath.WalkDir(staging, func(stagingPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(staging, stagingPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := ensureDirectoryAt(targetFD, rel, uint32(info.Mode().Perm())); err != nil {
				return fmt.Errorf("restorer: create directory %s: %w", filepath.Join(target, rel), err)
			}
			return nil
		}
		parentFD, _, err := openParentAt(targetFD, filepath.Dir(rel), true)
		if err != nil {
			return fmt.Errorf("restorer: prepare directory %s: %w", filepath.Join(target, filepath.Dir(rel)), err)
		}
		defer unix.Close(parentFD)
		name := filepath.Base(rel)
		if err := removeAllAt(parentFD, name); err != nil {
			return fmt.Errorf("restorer: replace %s: %w", filepath.Join(target, rel), err)
		}
		// WalkDir does not follow staged symlinks. Renameat writes through the
		// already-opened parent without resolving target path components again.
		if err := unix.Renameat(unix.AT_FDCWD, stagingPath, parentFD, name); err != nil {
			return fmt.Errorf("restorer: move %s into place: %w", stagingPath, err)
		}
		return nil
	})
}

// openParentAt opens rel beneath rootFD without following symlinks. In replace
// mode, missing or non-directory components are replaced with real
// directories; this is the commit-time recheck after overwrite confirmation.
func openParentAt(rootFD int, rel string, replace bool) (fd int, reachable bool, err error) {
	fd, err = unix.Dup(rootFD)
	if err != nil {
		return -1, false, err
	}
	if rel == "." || rel == "" {
		return fd, true, nil
	}
	for _, component := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		nextFD, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == nil {
			unix.Close(fd)
			fd = nextFD
			continue
		}
		if !replace {
			unix.Close(fd)
			if errors.Is(openErr, unix.ENOENT) || errors.Is(openErr, unix.ENOTDIR) || errors.Is(openErr, unix.ELOOP) {
				return -1, false, nil
			}
			return -1, false, openErr
		}
		if !errors.Is(openErr, unix.ENOENT) && !errors.Is(openErr, unix.ENOTDIR) && !errors.Is(openErr, unix.ELOOP) {
			unix.Close(fd)
			return -1, false, openErr
		}
		if err := removeAllAt(fd, component); err != nil {
			unix.Close(fd)
			return -1, false, err
		}
		if err := unix.Mkdirat(fd, component, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			unix.Close(fd)
			return -1, false, err
		}
		nextFD, err = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			unix.Close(fd)
			return -1, false, err
		}
		unix.Close(fd)
		fd = nextFD
	}
	return fd, true, nil
}

func ensureDirectoryAt(rootFD int, rel string, mode uint32) error {
	parentFD, _, err := openParentAt(rootFD, filepath.Dir(rel), true)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	name := filepath.Base(rel)
	var stat unix.Stat_t
	err = unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		return nil
	}
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err == nil {
		if err := removeAllAt(parentFD, name); err != nil {
			return err
		}
	}
	return unix.Mkdirat(parentFD, name, mode)
}

// removeAllAt removes name beneath parentFD without following any symlink.
// Directories are opened with O_NOFOLLOW and recursively removed by descriptor.
func removeAllAt(parentFD int, name string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(parentFD, name, 0)
	}
	dirFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(dirFD), name)
	entries, readErr := dir.ReadDir(-1)
	if readErr != nil {
		dir.Close()
		return readErr
	}
	for _, entry := range entries {
		if err := removeAllAt(dirFD, entry.Name()); err != nil {
			dir.Close()
			return err
		}
	}
	if err := dir.Close(); err != nil {
		return err
	}
	return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}
