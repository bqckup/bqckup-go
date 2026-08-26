// Package restorer walks a snapshot tree and writes its files into a
// staging directory, then moves the staged tree into the target. The
// repository is only ever read; nothing is written to it, and the target
// stays untouched on every abort path (staging + rename).
package restorer

import (
	"context"
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
	paths     []string        // cleaned configured include paths
	matched   map[string]bool // configured path -> some node matched it
	conflicts []string
	files     int
	bytes     int64
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
		if err := r.restoreNode(ctx, state, staging, targetAbs, anchor, effective); err != nil {
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

	if len(state.conflicts) > 0 && confirm != nil {
		if err := confirm(state.conflicts); err != nil {
			return Summary{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if err := r.moveIntoPlace(staging, targetAbs); err != nil {
		return Summary{}, err
	}
	return Summary{FilesRestored: state.files, BytesRestored: state.bytes, SkippedPaths: skipped}, nil
}

// restoreNode writes one node (when it belongs to a configured path) and
// recurses into its subtree. The absolute snapshot path of the node is
// passed down from the anchored root; the target layout mirrors it
// (restic layout: /var/www/html lands under <target>/var/www/html).
func (r *Restorer) restoreNode(ctx context.Context, st *restoreState, staging, targetAbs, absPath string, node *tree.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel := filepath.FromSlash(strings.TrimLeft(absPath, "/"))
	stagingPath := filepath.Join(staging, rel)
	targetPath := filepath.Join(targetAbs, rel)

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
			if info, err := os.Lstat(targetPath); err == nil && !info.IsDir() {
				st.conflicts = append(st.conflicts, targetPath)
			}
		case tree.TypeFile:
			if err := r.restoreFile(ctx, stagingPath, node); err != nil {
				return err
			}
			st.files++
			st.bytes += int64(node.Size)
			if _, err := os.Lstat(targetPath); err == nil {
				st.conflicts = append(st.conflicts, targetPath)
			}
		case tree.TypeSymlink:
			if err := os.Symlink(node.LinkTarget, stagingPath); err != nil {
				return fmt.Errorf("restorer: create symlink %s: %w", stagingPath, err)
			}
			if _, err := os.Lstat(targetPath); err == nil {
				st.conflicts = append(st.conflicts, targetPath)
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
			if err := r.restoreNode(ctx, st, staging, targetAbs, path.Join(absPath, child.Name), child); err != nil {
				return err
			}
		}
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

// moveIntoPlace puts the staged tree at the target. A missing target is
// one rename (staging lives next to it); an existing directory is merged
// entry by entry: files and symlinks are renamed individually (atomic
// replacement), directories are only created, never renamed over existing
// ones.
func (r *Restorer) moveIntoPlace(staging, target string) error {
	info, err := os.Lstat(target)
	if err == nil && !info.IsDir() {
		return fmt.Errorf("restorer: target %s exists and is not a directory", target)
	}
	if err != nil {
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
		info, err := entry.Info()
		if err != nil {
			return err
		}
		targetPath := filepath.Join(target, rel)
		if info.IsDir() {
			if err := os.MkdirAll(targetPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("restorer: create directory %s: %w", targetPath, err)
			}
			return nil
		}
		// WalkDir does not follow symlinks: files and symlinks both land here.
		if err := os.Rename(stagingPath, targetPath); err != nil {
			return fmt.Errorf("restorer: move %s into place: %w", stagingPath, err)
		}
		return nil
	})
}
