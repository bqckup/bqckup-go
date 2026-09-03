// Package archiver walks filesystems, chunks files, builds trees, and
// writes snapshots into a repository. Dedup is blob-level: unchanged
// content produces identical blob IDs and is never re-stored.
//
// Excludes support basename globs, include-root-relative patterns, absolute
// paths/patterns, and a trailing /** for recursive directories.
package archiver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/chunker"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/repository"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/tree"
	"github.com/bqckup/bqckup-go/internal/fileexclude"
)

// Archiver performs backups into an opened repository.
type Archiver struct {
	repo *repository.Repository
}

func New(repo *repository.Repository) *Archiver { return &Archiver{repo: repo} }

// BackupSpec describes one backup run.
type BackupSpec struct {
	Paths    []string
	Excludes []string
	Tags     []string
	Hostname string
	Username string
}

// Summary mirrors the counters the runner and history expect.
type Summary struct {
	SnapshotID          string
	FilesNew            int
	FilesChanged        int
	FilesUnmodified     int
	TotalFilesProcessed int
	TotalBytesProcessed int64
	DataAdded           int64
	TotalDuration       float64
	// Missed reports each blob re-stored because its ID was not in the
	// repository index (the dedup misses that make up DataAdded).
	Missed []MissedBlob
}

// MissedBlob describes one blob that had to be stored again.
type MissedBlob struct {
	Type incremental.BlobType
	Size int
}

// Backup walks the paths, stores all data, and writes one snapshot.
func (a *Archiver) Backup(ctx context.Context, spec BackupSpec) (incremental.ID, Summary, error) {
	if len(spec.Paths) == 0 {
		return incremental.ID{}, Summary{}, fmt.Errorf("archiver: at least one path is required")
	}
	// Fail loudly on malformed exclude patterns before any data is read
	// (restic rejects invalid patterns at startup too): a pattern that
	// never matches would silently back up files the user meant to exclude.
	for _, pattern := range spec.Excludes {
		if pattern == "" {
			continue
		}
		if err := fileexclude.Validate(pattern); err != nil {
			return incremental.ID{}, Summary{}, fmt.Errorf("archiver: invalid exclude pattern %q: %w", pattern, err)
		}
	}
	started := time.Now()
	parent, err := a.parentSnapshot(ctx, spec)
	if err != nil {
		return incremental.ID{}, Summary{}, err
	}
	state := &backupState{
		archiver: a,
		spec:     spec,
		started:  started,
		parent:   parent,
	}

	for i, path := range spec.Paths {
		if err := ctx.Err(); err != nil {
			return incremental.ID{}, Summary{}, err
		}
		if err := state.backupPathAt(ctx, path, fmt.Sprintf("%d", i)); err != nil {
			return incremental.ID{}, Summary{}, err
		}
	}

	rootTree, err := state.combineRoots(ctx)
	if err != nil {
		return incremental.ID{}, Summary{}, err
	}
	// Snapshot is written LAST: build every tree (including the synthetic
	// multi-root tree), then flush packs and indexes before the snapshot.
	if err := a.repo.Flush(ctx); err != nil {
		return incremental.ID{}, Summary{}, err
	}
	duration := time.Since(started).Seconds()
	// Persist the summary inside the snapshot document (restic 0.19 writes
	// it too), so listing snapshots can report sizes without a live run.
	snap := snapshot.Snapshot{
		Time:           started.UTC(),
		Parent:         parentID(parent),
		Tree:           rootTree,
		Paths:          spec.Paths,
		Hostname:       spec.Hostname,
		Username:       spec.Username,
		UID:            uint32(os.Getuid()),
		GID:            uint32(os.Getgid()),
		Excludes:       spec.Excludes,
		Tags:           spec.Tags,
		ProgramVersion: "bqckup",
		Summary: &snapshot.Summary{
			FilesNew:            state.filesNew,
			FilesChanged:        state.filesChanged,
			FilesUnmodified:     state.filesUnmodified,
			DataAdded:           uint64(state.dataAdded),
			TotalFilesProcessed: state.filesNew + state.filesChanged + state.filesUnmodified,
			TotalBytesProcessed: uint64(state.bytesProcessed),
			TotalDuration:       duration,
		},
	}
	snapID, err := a.repo.SaveSnapshot(ctx, snap)
	if err != nil {
		return incremental.ID{}, Summary{}, err
	}

	summary := Summary{
		SnapshotID:          snapID.String(),
		FilesNew:            state.filesNew,
		FilesChanged:        state.filesChanged,
		FilesUnmodified:     state.filesUnmodified,
		TotalFilesProcessed: state.filesNew + state.filesChanged + state.filesUnmodified,
		TotalBytesProcessed: state.bytesProcessed,
		DataAdded:           state.dataAdded,
		Missed:              state.missed,
		TotalDuration:       duration,
	}
	return snapID, summary, nil
}

type backupState struct {
	archiver        *Archiver
	spec            BackupSpec
	started         time.Time
	rootTrees       []incremental.ID
	filesNew        int
	filesChanged    int
	filesUnmodified int
	bytesProcessed  int64
	dataAdded       int64
	missed          []MissedBlob
	parent          *parentState
}

// parentState contains the previous matching snapshot's root nodes. Directory
// trees are loaded lazily while the current filesystem walk reaches them, so a
// large parent snapshot does not require a second in-memory copy of every node.
type parentState struct {
	id    incremental.ID
	repo  *repository.Repository
	roots map[string]*tree.Node
}

func parentID(parent *parentState) *incremental.ID {
	if parent == nil {
		return nil
	}
	id := parent.id
	return &id
}

func (a *Archiver) parentSnapshot(ctx context.Context, spec BackupSpec) (*parentState, error) {
	snapshots, err := a.repo.ListSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("archiver: list parent snapshots: %w", err)
	}
	var selected *repository.SnapshotWithID
	for i := range snapshots {
		candidate := &snapshots[i]
		if !sameStrings(candidate.Snapshot.Paths, spec.Paths) || !sameStrings(candidate.Snapshot.Excludes, spec.Excludes) {
			continue
		}
		if selected == nil || candidate.Snapshot.Time.After(selected.Snapshot.Time) {
			selected = candidate
		}
	}
	if selected == nil || selected.Snapshot.Tree == nil {
		return nil, nil
	}

	parent := &parentState{
		id:    selected.ID,
		repo:  a.repo,
		roots: make(map[string]*tree.Node),
	}
	root, err := a.repo.LoadTree(ctx, *selected.Snapshot.Tree)
	if err != nil {
		return nil, fmt.Errorf("archiver: load parent tree: %w", err)
	}
	if len(spec.Paths) == 1 {
		if len(root.Nodes) != 1 {
			// Official Restic snapshots may represent a single include root
			// without Bqckup's wrapper node. They remain valid parents, but
			// cannot be indexed by this fast path, so fall back to a normal walk.
			return nil, nil
		}
		parent.roots["0"] = root.Nodes[0]
		return parent, nil
	}

	rootNames := uniqueRootNames(spec.Paths)
	byName := make(map[string]*tree.Node, len(root.Nodes))
	for _, node := range root.Nodes {
		byName[node.Name] = node
	}
	for i, name := range rootNames {
		wrapper, ok := byName[name]
		if !ok {
			return nil, nil
		}
		if wrapper.Subtree == nil {
			return nil, nil
		}
		rootTree, err := parent.loadTree(ctx, wrapper.Subtree)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, nil
		}
		if len(rootTree.Nodes) != 1 {
			return nil, nil
		}
		parent.roots[fmt.Sprintf("%d", i)] = rootTree.Nodes[0]
	}
	return parent, nil
}

func (p *parentState) loadTree(ctx context.Context, id *incremental.ID) (*tree.Tree, error) {
	return p.repo.LoadTree(ctx, *id)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func uniqueRootNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	used := make(map[string]bool, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		name := base
		for suffix := 1; used[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		used[name] = true
		names = append(names, name)
	}
	return names
}

// backupPath backs up one root path into a tree blob.
func (s *backupState) backupPath(ctx context.Context, path string) error {
	return s.backupPathAt(ctx, path, "0")
}

func (s *backupState) backupPathAt(ctx context.Context, path, key string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("archiver: stat %s: %w", path, err)
	}
	rootNode, _, err := s.nodeForAt(ctx, path, info, key, s.parentRoot(key))
	if err != nil {
		return err
	}
	if rootNode.Name == "" {
		rootNode.Name = filepath.Base(path)
	}
	rootTree := &tree.Tree{Nodes: []*tree.Node{rootNode}}
	doc, err := rootTree.Marshal()
	if err != nil {
		return err
	}
	id, err := s.saveBlob(ctx, incremental.TreeBlob, doc)
	if err != nil {
		return err
	}
	s.rootTrees = append(s.rootTrees, id)
	return nil
}

// nodeFor builds the tree node for one filesystem object.
func (s *backupState) nodeFor(ctx context.Context, path string, info os.FileInfo) (*tree.Node, error) {
	node, _, err := s.nodeForAt(ctx, path, info, "", nil)
	return node, err
}

func (s *backupState) nodeForAt(ctx context.Context, path string, info os.FileInfo, key string, old *tree.Node) (*tree.Node, bool, error) {
	// atime is deliberately not recorded (restic default): reading a file
	// during backup updates it on relatime filesystems, which would change
	// every tree on the next run and break dedup.
	node := &tree.Node{
		Name:       info.Name(),
		Mode:       info.Mode(),
		ModTime:    info.ModTime(),
		ChangeTime: info.ModTime(),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		node.UID = stat.Uid
		node.GID = stat.Gid
		node.Inode = stat.Ino
		node.DeviceID = uint64(stat.Dev)
		node.Links = uint64(stat.Nlink)
		node.Size = uint64(stat.Size)
		node.ChangeTime = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return nil, false, fmt.Errorf("archiver: readlink %s: %w", path, err)
		}
		node.Type = tree.TypeSymlink
		node.LinkTarget = target
		return node, sameMetadata(node, old), nil

	case info.IsDir():
		subtree, unchanged, err := s.dirTreeAt(ctx, path, key, old)
		if err != nil {
			return nil, false, err
		}
		node.Type = tree.TypeDir
		node.Subtree = subtree
		unchanged = unchanged && sameMetadata(node, old)
		if unchanged {
			node = cloneNode(old)
		}
		return node, unchanged, nil

	case info.Mode().IsRegular():
		node.Type = tree.TypeFile
		if old != nil && old.Type == tree.TypeFile && old.Content != nil && sameMetadata(node, old) {
			node.Content = append([]incremental.ID{}, old.Content...)
			s.filesUnmodified++
			s.bytesProcessed += int64(node.Size)
			return node, true, nil
		}
		content, err := s.saveFile(ctx, path)
		if err != nil {
			return nil, false, err
		}
		node.Content = content
		if old != nil {
			s.filesChanged++
		} else {
			s.filesNew++
		}
		return node, false, nil

	default:
		// sockets, fifos, devices: record with metadata only, like restic
		// stores irregular types
		switch {
		case info.Mode()&os.ModeCharDevice != 0:
			node.Type = tree.TypeCharDev
		case info.Mode()&os.ModeDevice != 0:
			node.Type = tree.TypeDev
		case info.Mode()&os.ModeNamedPipe != 0:
			node.Type = tree.TypeFIFO
		case info.Mode()&os.ModeSocket != 0:
			node.Type = tree.TypeSocket
		default:
			node.Type = tree.TypeIrregular
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			node.Device = uint64(stat.Rdev)
		}
		return node, sameMetadata(node, old), nil
	}
}

// dirTree walks one directory and returns the subtree blob ID.
func (s *backupState) dirTree(ctx context.Context, dir string) (*incremental.ID, error) {
	id, _, err := s.dirTreeAt(ctx, dir, "", nil)
	return id, err
}

func (s *backupState) dirTreeAt(ctx context.Context, dir, key string, old *tree.Node) (*incremental.ID, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("archiver: read dir %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	tr := &tree.Tree{}
	allUnmodified := true
	seen := make(map[string]struct{}, len(entries))
	var oldTree *tree.Tree
	if old != nil && old.Subtree != nil {
		oldTree, err = s.parent.loadTree(ctx, old.Subtree)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, false, ctxErr
			}
			oldTree = nil
		}
	}
	oldChildren := make(map[string]*tree.Node)
	if oldTree != nil {
		for _, oldNode := range oldTree.Nodes {
			oldChildren[oldNode.Name] = oldNode
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		path := filepath.Join(dir, entry.Name())
		if s.excluded(path) {
			continue
		}
		seen[entry.Name()] = struct{}{}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, false, fmt.Errorf("archiver: stat %s: %w", path, err)
		}
		node, unchanged, err := s.nodeForAt(ctx, path, info, key+"/"+entry.Name(), oldChildren[entry.Name()])
		if err != nil {
			return nil, false, err
		}
		allUnmodified = allUnmodified && unchanged
		if err := tr.Add(node); err != nil {
			return nil, false, err
		}
	}
	if oldTree == nil || len(oldTree.Nodes) != len(seen) {
		allUnmodified = false
	} else {
		for _, oldNode := range oldTree.Nodes {
			if _, ok := seen[oldNode.Name]; !ok {
				allUnmodified = false
				break
			}
		}
	}
	if allUnmodified {
		if old != nil && old.Subtree != nil {
			id := *old.Subtree
			return &id, true, nil
		}
	}
	doc, err := tr.Marshal()
	if err != nil {
		return nil, false, err
	}
	id, err := s.saveBlob(ctx, incremental.TreeBlob, doc)
	if err != nil {
		return nil, false, err
	}
	return &id, false, nil
}

func (s *backupState) parentRoot(key string) *tree.Node {
	if s.parent == nil {
		return nil
	}
	return s.parent.roots[key]
}

func cloneNode(node *tree.Node) *tree.Node {
	copy := *node
	if node.Content != nil {
		copy.Content = append([]incremental.ID{}, node.Content...)
	}
	return &copy
}

func sameMetadata(a, b *tree.Node) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Type == b.Type && a.Mode == b.Mode && a.ModTime.Equal(b.ModTime) &&
		a.ChangeTime.Equal(b.ChangeTime) && a.UID == b.UID && a.GID == b.GID && a.Inode == b.Inode &&
		a.DeviceID == b.DeviceID && a.Size == b.Size && a.Links == b.Links && a.LinkTarget == b.LinkTarget &&
		a.LinkTargetRaw == b.LinkTargetRaw && a.Device == b.Device
}

// saveFile chunks a regular file and returns its content blob IDs.
func (s *backupState) saveFile(ctx context.Context, path string) ([]incremental.ID, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archiver: open %s: %w", path, err)
	}
	defer file.Close()

	polynomial := s.archiver.repo.Config().ChunkerPolynomial
	chunker := chunker.New(file, polynomial)
	// non-nil: restic check rejects files with a nil blob list
	content := make([]incremental.ID, 0)
	var buffer []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := chunker.Next(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archiver: chunk %s: %w", path, err)
		}
		id, err := s.saveBlob(ctx, incremental.DataBlob, chunk.Data)
		if err != nil {
			return nil, err
		}
		content = append(content, id)
		s.bytesProcessed += int64(len(chunk.Data))
		buffer = chunk.Data
	}
	return content, nil
}

// combineRoots returns the snapshot tree: the single root tree, or a
// synthetic top-level tree listing multiple roots by name.
func (s *backupState) combineRoots(ctx context.Context) (*incremental.ID, error) {
	if len(s.rootTrees) == 1 {
		id := s.rootTrees[0]
		return &id, nil
	}
	tr := &tree.Tree{}
	for i, name := range uniqueRootNames(s.spec.Paths) {
		subtree := s.rootTrees[i]
		if err := tr.Add(&tree.Node{Name: name, Type: tree.TypeDir, Subtree: &subtree}); err != nil {
			return nil, fmt.Errorf("archiver: combine roots: %w", err)
		}
	}
	doc, err := tr.Marshal()
	if err != nil {
		return nil, err
	}
	id, err := s.saveBlob(ctx, incremental.TreeBlob, doc)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// saveBlob stores a blob and counts new bytes for the summary.
func (s *backupState) saveBlob(ctx context.Context, blobType incremental.BlobType, data []byte) (incremental.ID, error) {
	id := incremental.Hash(data)
	_, exists := s.archiver.repo.MasterIndex().Lookup(blobType, id)
	if !exists {
		s.dataAdded += int64(len(data))
		s.missed = append(s.missed, MissedBlob{Type: blobType, Size: len(data)})
	}
	return s.archiver.repo.SaveBlob(ctx, blobType, data)
}

// excluded reports whether path matches a basename glob, an include-root
// relative pattern, or an absolute path/pattern.
func (s *backupState) excluded(path string) bool {
	return fileexclude.MatchAny(s.spec.Excludes, path, s.spec.Paths)
}
