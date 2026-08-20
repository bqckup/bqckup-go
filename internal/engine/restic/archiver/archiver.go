// Package archiver walks filesystems, chunks files, builds trees, and
// writes snapshots into a repository. Dedup is blob-level: unchanged
// content produces identical blob IDs and is never re-stored.
//
// L1 scope notes:
//   - No parent-snapshot comparison: every file is counted files_new and
//     trees are rebuilt each run (identical trees dedup by blob ID anyway).
//   - Excludes use filepath.Match against the basename and the full path
//     (simpler than restic's pattern engine; upgrade when a real need shows).
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

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/chunker"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/bqckup/bqckup-go/internal/engine/restic/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
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
}

// Backup walks the paths, stores all data, and writes one snapshot.
func (a *Archiver) Backup(ctx context.Context, spec BackupSpec) (restic.ID, Summary, error) {
	if len(spec.Paths) == 0 {
		return restic.ID{}, Summary{}, fmt.Errorf("archiver: at least one path is required")
	}
	started := time.Now()
	state := &backupState{
		archiver: a,
		spec:     spec,
		started:  started,
	}

	for _, path := range spec.Paths {
		if err := ctx.Err(); err != nil {
			return restic.ID{}, Summary{}, err
		}
		if err := state.backupPath(ctx, path); err != nil {
			return restic.ID{}, Summary{}, err
		}
	}

	// Snapshot is written LAST: packs and index are flushed first, so an
	// interrupted run never leaves a snapshot referencing missing data.
	if err := a.repo.Flush(ctx); err != nil {
		return restic.ID{}, Summary{}, err
	}
	rootTree, err := state.combineRoots(ctx)
	if err != nil {
		return restic.ID{}, Summary{}, err
	}
	snap := snapshot.Snapshot{
		Time:           started.UTC(),
		Tree:           rootTree,
		Paths:          spec.Paths,
		Hostname:       spec.Hostname,
		Username:       spec.Username,
		UID:            uint32(os.Getuid()),
		GID:            uint32(os.Getgid()),
		Excludes:       spec.Excludes,
		Tags:           spec.Tags,
		ProgramVersion: "bqckup",
	}
	snapID, err := a.repo.SaveSnapshot(ctx, snap)
	if err != nil {
		return restic.ID{}, Summary{}, err
	}

	duration := time.Since(started).Seconds()
	summary := Summary{
		SnapshotID:          snapID.String(),
		FilesNew:            state.filesNew,
		FilesChanged:        state.filesChanged,
		FilesUnmodified:     state.filesUnmodified,
		TotalFilesProcessed: state.filesNew + state.filesChanged + state.filesUnmodified,
		TotalBytesProcessed: state.bytesProcessed,
		DataAdded:           state.dataAdded,
		TotalDuration:       duration,
	}
	return snapID, summary, nil
}

type backupState struct {
	archiver        *Archiver
	spec            BackupSpec
	started         time.Time
	rootTrees       []restic.ID
	filesNew        int
	filesChanged    int
	filesUnmodified int
	bytesProcessed  int64
	dataAdded       int64
}

// backupPath backs up one root path into a tree blob.
func (s *backupState) backupPath(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("archiver: stat %s: %w", path, err)
	}
	rootNode, err := s.nodeFor(ctx, path, info)
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
	id, err := s.saveBlob(ctx, restic.TreeBlob, doc)
	if err != nil {
		return err
	}
	s.rootTrees = append(s.rootTrees, id)
	return nil
}

// nodeFor builds the tree node for one filesystem object.
func (s *backupState) nodeFor(ctx context.Context, path string, info os.FileInfo) (*tree.Node, error) {
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
			return nil, fmt.Errorf("archiver: readlink %s: %w", path, err)
		}
		node.Type = tree.TypeSymlink
		node.LinkTarget = target
		return node, nil

	case info.IsDir():
		subtree, err := s.dirTree(ctx, path)
		if err != nil {
			return nil, err
		}
		node.Type = tree.TypeDir
		node.Subtree = subtree
		return node, nil

	case info.Mode().IsRegular():
		content, err := s.saveFile(ctx, path)
		if err != nil {
			return nil, err
		}
		node.Type = tree.TypeFile
		node.Content = content
		s.filesNew++ // no parent comparison in L1: every file is new
		return node, nil

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
		return node, nil
	}
}

// dirTree walks one directory and returns the subtree blob ID.
func (s *backupState) dirTree(ctx context.Context, dir string) (*restic.ID, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("archiver: read dir %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	tr := &tree.Tree{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, entry.Name())
		if s.excluded(path) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("archiver: stat %s: %w", path, err)
		}
		node, err := s.nodeFor(ctx, path, info)
		if err != nil {
			return nil, err
		}
		if err := tr.Add(node); err != nil {
			return nil, err
		}
	}
	doc, err := tr.Marshal()
	if err != nil {
		return nil, err
	}
	id, err := s.saveBlob(ctx, restic.TreeBlob, doc)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// saveFile chunks a regular file and returns its content blob IDs.
func (s *backupState) saveFile(ctx context.Context, path string) ([]restic.ID, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archiver: open %s: %w", path, err)
	}
	defer file.Close()

	polynomial := s.archiver.repo.Config().ChunkerPolynomial
	chunker := chunker.New(file, polynomial)
	// non-nil: restic check rejects files with a nil blob list
	content := make([]restic.ID, 0)
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
		id, err := s.saveBlob(ctx, restic.DataBlob, chunk.Data)
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
func (s *backupState) combineRoots(ctx context.Context) (*restic.ID, error) {
	if len(s.rootTrees) == 1 {
		id := s.rootTrees[0]
		return &id, nil
	}
	tr := &tree.Tree{}
	for i, path := range s.spec.Paths {
		subtree := s.rootTrees[i]
		if err := tr.Add(&tree.Node{Name: filepath.Base(path), Type: tree.TypeDir, Subtree: &subtree}); err != nil {
			return nil, err
		}
	}
	doc, err := tr.Marshal()
	if err != nil {
		return nil, err
	}
	id, err := s.saveBlob(ctx, restic.TreeBlob, doc)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// saveBlob stores a blob and counts new bytes for the summary.
func (s *backupState) saveBlob(ctx context.Context, blobType restic.BlobType, data []byte) (restic.ID, error) {
	id := restic.Hash(data)
	if _, exists := s.archiver.repo.MasterIndex().Lookup(id); !exists {
		s.dataAdded += int64(len(data))
	}
	return s.archiver.repo.SaveBlob(ctx, blobType, data)
}

// excluded reports whether path matches any exclude pattern. L1 matches the
// basename and the full path with filepath.Match.
func (s *backupState) excluded(path string) bool {
	for _, pattern := range s.spec.Excludes {
		if pattern == "" {
			continue
		}
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
	}
	return false
}
