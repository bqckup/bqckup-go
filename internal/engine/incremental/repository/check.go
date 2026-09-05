package repository

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/index"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/pack"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/tree"
	"github.com/klauspost/compress/zstd"
)

// FindingType names one class of defect the repository check reports.
type FindingType string

const (
	FindingBrokenKey      FindingType = "broken_key"      // unreadable or unopenable key file
	FindingBrokenConfig   FindingType = "broken_config"   // config cannot be decrypted, parsed, validated, or matches no key
	FindingBrokenIndex    FindingType = "broken_index"    // index file cannot be loaded
	FindingBrokenSnapshot FindingType = "broken_snapshot" // snapshot document is unreadable or has no tree
	FindingMissingBlob    FindingType = "missing_blob"    // tree references a blob with no index entry
	FindingCorruptBlob    FindingType = "corrupt_blob"    // blob fails to decrypt, decompress, or match its id
	FindingMissingPack    FindingType = "missing_pack"    // index references a pack that is not stored
	FindingBrokenPack     FindingType = "broken_pack"     // pack header does not match its index entries
	FindingOrphanedPack   FindingType = "orphaned_pack"   // stored pack no index references
)

// Finding is one defect the check found. ID names the object (the full hex
// storage ID, or the literal "config" for the config file). Missing and
// corrupt blobs additionally carry the snapshot they were found under or
// the pack that holds them; a missing pack carries how many index entries
// it should hold. Detail holds a safe message only (object IDs, counts) —
// never errors, paths, or secrets.
type Finding struct {
	Type       FindingType
	ID         string
	SnapshotID string
	PackID     string
	BlobCount  int
	Detail     string
}

// CheckResult is the outcome of one repository check. The counts report
// the objects examined: index files, snapshots, indexed packs, and
// distinct indexed blobs. Findings are sorted by type then ID.
type CheckResult struct {
	Indexes   int
	Snapshots int
	Packs     int
	Blobs     int
	Findings  []Finding
}

// CheckOpen opens a repository for checking with a tolerant open: every
// key file is validated, one must open with the password, the config must
// decrypt and validate with the opened key (its ID must match that key's
// hash), and every index file must load. Problems become findings instead
// of errors, and a broken index file never aborts loading of the others.
// A repository with no openable key returns a nil Repository with the key
// findings; the strict Open path (backup, retention, restore) is unchanged.
func CheckOpen(ctx context.Context, b backend.Backend, password string) (*Repository, []Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if _, err := b.Stat(ctx, incremental.Handle{Type: incremental.ConfigFile}); b.IsNotExist(err) {
		return nil, nil, fmt.Errorf("%w: no config file found", incremental.ErrRepoNotFound)
	}
	master, findings, err := openKeysTolerant(ctx, b, password)
	if err != nil {
		return nil, findings, err
	}
	if master == nil {
		return nil, findings, nil // no key opened: nothing else can be decrypted
	}
	config, configFindings, err := loadConfigTolerant(ctx, b, master)
	findings = append(findings, configFindings...)
	if err != nil {
		return nil, findings, err
	}
	repo, err := newRepository(b, master, config)
	if err != nil {
		return nil, findings, err
	}
	indexFindings, err := loadIndexesTolerant(ctx, repo)
	findings = append(findings, indexFindings...)
	if err != nil {
		return nil, findings, err
	}
	return repo, findings, nil
}

// openKeysTolerant lists every key file and validates each one: a key that
// cannot be read, parsed, or opened with the password becomes a broken_key
// finding and the next key is tried. The first key that opens supplies the
// master key; a nil key means no key opened at all.
func openKeysTolerant(ctx context.Context, b backend.Backend, password string) (*crypto.MasterKey, []Finding, error) {
	var handles []incremental.Handle
	if err := b.List(ctx, incremental.KeyFileType, func(h incremental.Handle, _ int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("repository: list key files: %w", err)
	}
	var findings []Finding
	var master *crypto.MasterKey
	for _, h := range handles {
		var doc []byte
		err := b.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			var err error
			doc, err = io.ReadAll(rd)
			return err
		})
		if err != nil {
			findings = append(findings, Finding{Type: FindingBrokenKey, ID: h.Name, Detail: "key file could not be read"})
			continue
		}
		var keyFile crypto.KeyFile
		if err := json.Unmarshal(doc, &keyFile); err != nil {
			findings = append(findings, Finding{Type: FindingBrokenKey, ID: h.Name, Detail: "key file is not valid key JSON"})
			continue
		}
		opened, err := keyFile.MasterKey(password)
		if err != nil {
			findings = append(findings, Finding{Type: FindingBrokenKey, ID: h.Name, Detail: "key file does not open with the repository password"})
			continue
		}
		if master == nil {
			master = opened
		}
	}
	return master, findings, nil
}

// loadConfigTolerant reads, decrypts, and validates the config document.
// Every failure becomes a broken_config finding; a decrypt, parse, or
// validation failure returns an empty config so the check can continue
// (the master key alone opens the index files). A config whose ID does not
// match the master key's hash is a finding too, but the config stays usable.
func loadConfigTolerant(ctx context.Context, b backend.Backend, master *crypto.MasterKey) (Config, []Finding, error) {
	var raw []byte
	err := b.Load(ctx, incremental.Handle{Type: incremental.ConfigFile}, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return Config{}, []Finding{{Type: FindingBrokenConfig, ID: "config", Detail: "config file could not be read"}}, nil
	}
	plain, err := master.Open(nil, raw)
	if err != nil {
		return Config{}, []Finding{{Type: FindingBrokenConfig, ID: "config", Detail: "config file does not decrypt with the repository key"}}, nil
	}
	var config Config
	if err := json.Unmarshal(plain, &config); err != nil {
		return Config{}, []Finding{{Type: FindingBrokenConfig, ID: "config", Detail: "config file is not valid JSON"}}, nil
	}
	if err := config.Validate(); err != nil {
		return Config{}, []Finding{{Type: FindingBrokenConfig, ID: "config", Detail: "config file failed validation"}}, nil
	}
	want := incremental.Hash(master.Encrypt[:])
	if config.ID != want {
		return config, []Finding{{Type: FindingBrokenConfig, ID: "config",
			Detail: fmt.Sprintf("config id %s does not match the repository key", config.ID)}}, nil
	}
	return config, nil, nil
}

// loadIndexesTolerant loads every index file into the master index. A file
// that cannot be read, decrypted, or parsed becomes a broken_index finding;
// loading continues with the healthy files. The returned findings carry
// each file's storage ID.
func loadIndexesTolerant(ctx context.Context, repo *Repository) ([]Finding, error) {
	var handles []incremental.Handle
	if err := repo.backend.List(ctx, incremental.IndexFile, func(h incremental.Handle, _ int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("repository: list index files: %w", err)
	}
	var findings []Finding
	for _, h := range handles {
		var raw []byte
		err := repo.backend.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			var err error
			raw, err = io.ReadAll(rd)
			return err
		})
		if err == nil {
			var doc index.Index
			doc, err = index.Open(raw, repo.master)
			if err == nil {
				repo.index.AddIndex(doc)
				repo.checkIndexDocs = append(repo.checkIndexDocs, doc)
			}
		}
		if err != nil {
			findings = append(findings, Finding{Type: FindingBrokenIndex, ID: h.Name, Detail: "index file could not be loaded"})
		}
	}
	repo.checkIndexFiles = len(handles)
	return findings, nil
}

// CheckRepository runs the structural and, when readData is set, the
// data-authentication half of a repository check on a repository opened by
// CheckOpen. repo may be nil — when no key file opened the open findings
// (broken_key) are the whole result. The check never writes to the backend.
// Locking is the caller's concern (non-exclusive, like listing).
func CheckRepository(ctx context.Context, repo *Repository, openFindings []Finding, readData bool) (CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return CheckResult{}, err
	}
	result := CheckResult{Findings: append([]Finding(nil), openFindings...)}
	if repo == nil {
		sortFindings(result.Findings)
		return result, nil
	}
	result.Blobs = repo.index.Len()

	packs := mergedPacks(repo.checkIndexDocs)
	result.Packs = len(packs)
	result.Indexes = repo.checkIndexFiles

	if err := repo.checkSnapshots(ctx, &result); err != nil {
		return CheckResult{}, err
	}
	if err := repo.checkPacks(ctx, &result, packs); err != nil {
		return CheckResult{}, err
	}
	if err := repo.checkOrphans(ctx, &result, packs); err != nil {
		return CheckResult{}, err
	}
	if readData {
		if err := repo.checkBlobData(ctx, &result, packs); err != nil {
			return CheckResult{}, err
		}
	}
	sortFindings(result.Findings)
	return result, nil
}

// mergedPacks merges every index document into one pack map. Each pack is
// listed once; duplicate blob entries across index files (rewrites, crash
// leftovers) collapse into one, keyed by location.
func mergedPacks(docs []index.Index) map[incremental.ID]*[]index.Blob {
	packs := make(map[incremental.ID]*[]index.Blob)
	for _, doc := range docs {
		for _, p := range doc.Packs {
			list := packs[p.ID]
			if list == nil {
				list = new([]index.Blob)
				packs[p.ID] = list
			}
			for _, blob := range p.Blobs {
				duplicate := false
				for _, existing := range *list {
					if existing.ID == blob.ID && existing.Offset == blob.Offset && existing.Length == blob.Length {
						duplicate = true
						break
					}
				}
				if !duplicate {
					*list = append(*list, blob)
				}
			}
		}
	}
	return packs
}

// checkSnapshots loads every snapshot document. An unreadable document or
// one without a tree is a broken_snapshot finding; every other snapshot's
// tree is walked for the reachable set, converting load and parse failures
// into missing_blob and corrupt_blob findings that name the snapshot.
func (r *Repository) checkSnapshots(ctx context.Context, result *CheckResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var handles []incremental.Handle
	if err := r.backend.List(ctx, incremental.SnapshotFile, func(h incremental.Handle, _ int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return fmt.Errorf("repository: list snapshots for check: %w", err)
	}
	result.Snapshots = len(handles)
	seenTrees := make(map[incremental.ID]struct{})
	for _, h := range handles {
		loaded, finding := r.loadSnapshotForCheck(ctx, h)
		if finding.Type != "" {
			result.Findings = append(result.Findings, finding)
			continue
		}
		if loaded.Snapshot.Tree == nil {
			result.Findings = append(result.Findings, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot has no tree"})
			continue
		}
		if err := r.walkForCheck(ctx, *loaded.Snapshot.Tree, h.Name, seenTrees, &result.Findings); err != nil {
			return err
		}
	}
	return nil
}

// loadSnapshotForCheck reads, decrypts, and parses one snapshot document,
// returning a Finding instead of an error on any failure.
func (r *Repository) loadSnapshotForCheck(ctx context.Context, h incremental.Handle) (SnapshotWithID, Finding) {
	var raw []byte
	err := r.backend.Load(ctx, h, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot could not be read"}
	}
	plain, err := r.master.Open(nil, raw)
	if err != nil {
		return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot does not decrypt with the repository key"}
	}
	payload := plain
	if len(payload) > 0 {
		switch payload[0] {
		case 0x01:
			payload = payload[1:]
		case 0x02:
			decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot payload is not valid"}
			}
			defer decoder.Close()
			payload, err = decoder.DecodeAll(payload[1:], nil)
			if err != nil {
				return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot payload is not valid zstd"}
			}
		}
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot is not valid JSON"}
	}
	if _, err := incremental.ParseID(h.Name); err != nil {
		return SnapshotWithID{}, Finding{Type: FindingBrokenSnapshot, ID: h.Name, Detail: "snapshot file name is not an object id"}
	}
	return SnapshotWithID{Snapshot: snap}, Finding{}
}

// walkForCheck walks one tree and every subtree, recording reachable blobs
// that the index does not know (missing_blob) and tree blobs that fail to
// decrypt or parse (corrupt_blob), each under the owning snapshot. Unlike
// markTree it never stops at the first failure: every node is examined and
// every finding recorded.
func (r *Repository) walkForCheck(ctx context.Context, treeID incremental.ID, snapshotID string, seen map[incremental.ID]struct{}, out *[]Finding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, visited := seen[treeID]; visited {
		return nil
	}
	seen[treeID] = struct{}{}
	if _, ok := r.index.Lookup(incremental.TreeBlob, treeID); !ok {
		*out = append(*out, Finding{Type: FindingMissingBlob, ID: treeID.String(), SnapshotID: snapshotID})
		return nil
	}
	plain, err := r.loadBlob(ctx, incremental.TreeBlob, treeID)
	if err != nil {
		*out = append(*out, Finding{Type: FindingCorruptBlob, ID: treeID.String(), SnapshotID: snapshotID, Detail: "tree could not be loaded"})
		return nil
	}
	t, err := tree.Unmarshal(plain)
	if err != nil {
		*out = append(*out, Finding{Type: FindingCorruptBlob, ID: treeID.String(), SnapshotID: snapshotID, Detail: "tree payload is not a valid tree"})
		return nil
	}
	for _, node := range t.Nodes {
		if node.Subtree != nil {
			if err := r.walkForCheck(ctx, *node.Subtree, snapshotID, seen, out); err != nil {
				return err
			}
		}
		for _, contentID := range node.Content {
			if _, ok := r.index.Lookup(incremental.DataBlob, contentID); !ok {
				*out = append(*out, Finding{Type: FindingMissingBlob, ID: contentID.String(), SnapshotID: snapshotID})
			}
		}
	}
	return nil
}

// headerBlob is the comparable view of one blob for pack header matching.
type headerBlob struct {
	ID           incremental.ID
	Offset       uint32
	Length       uint32
	Compressed   bool
	Uncompressed uint32
}

// checkPacks stats every indexed pack and verifies each stored pack's
// header against the index: same blob IDs at the same offsets with the
// same lengths. A missing pack is a missing_pack finding carrying the
// number of blobs the index records for it; a header that cannot be read,
// decrypted, parsed, or matched is a broken_pack finding.
func (r *Repository) checkPacks(ctx context.Context, result *CheckResult, packs map[incremental.ID]*[]index.Blob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	packIDs := make([]incremental.ID, 0, len(packs))
	for id := range packs {
		packIDs = append(packIDs, id)
	}
	sort.Slice(packIDs, func(i, j int) bool { return bytes.Compare(packIDs[i][:], packIDs[j][:]) < 0 })
	for _, packID := range packIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := incremental.Handle{Type: incremental.DataFile, Name: packID.String()}
		info, err := r.backend.Stat(ctx, h)
		if r.backend.IsNotExist(err) {
			result.Findings = append(result.Findings, Finding{Type: FindingMissingPack, ID: packID.String(), BlobCount: len(*packs[packID])})
			continue
		}
		if err != nil {
			return fmt.Errorf("repository: stat pack %s: %w", packID, err)
		}
		if ok := r.verifyPackHeader(ctx, h, info.Size, *packs[packID]); !ok {
			result.Findings = append(result.Findings, Finding{Type: FindingBrokenPack, ID: packID.String(), Detail: "pack header does not match its index entries"})
		}
	}
	return nil
}

// verifyPackHeader reads and decrypts one pack's header and compares its
// entries with the index entries for that pack.
func (r *Repository) verifyPackHeader(ctx context.Context, h incremental.Handle, size int64, indexed []index.Blob) bool {
	read := func(length int, offset int64) ([]byte, bool) {
		var out []byte
		err := r.backend.Load(ctx, h, length, offset, func(rd io.Reader) error {
			var err error
			out, err = io.ReadAll(rd)
			return err
		})
		return out, err == nil
	}
	if size < 4 {
		return false
	}
	trailer, ok := read(4, size-4)
	if !ok {
		return false
	}
	headerLength := int(binary.LittleEndian.Uint32(trailer))
	if headerLength < crypto.Extension || int64(headerLength)+4 > size {
		return false
	}
	ciphertext, ok := read(headerLength, size-4-int64(headerLength))
	if !ok {
		return false
	}
	plain, err := r.master.Open(nil, ciphertext)
	if err != nil {
		return false
	}
	entries, err := pack.ParseHeader(plain)
	if err != nil {
		return false
	}
	got := make([]headerBlob, len(entries))
	for i, entry := range entries {
		got[i] = headerBlob{
			ID:           entry.ID,
			Offset:       entry.Offset,
			Length:       entry.Length,
			Compressed:   entry.Type == incremental.CompressedDataBlob || entry.Type == incremental.CompressedTreeBlob,
			Uncompressed: entry.UncompressedLength,
		}
	}
	want := make([]headerBlob, 0, len(indexed))
	for _, entry := range indexed {
		want = append(want, headerBlob{
			ID:           entry.ID,
			Offset:       entry.Offset,
			Length:       entry.Length,
			Compressed:   entry.UncompressedLength > 0,
			Uncompressed: entry.UncompressedLength,
		})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Offset < want[j].Offset })
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// checkOrphans lists the data area for packs no index references. File
// names that do not parse as object ids are skipped, exactly as prune
// leaves them alone.
func (r *Repository) checkOrphans(ctx context.Context, result *CheckResult, packs map[incremental.ID]*[]index.Blob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.backend.List(ctx, incremental.DataFile, func(h incremental.Handle, _ int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		packID, err := incremental.ParseID(h.Name)
		if err != nil {
			return nil // not a pack name; leave it alone
		}
		if _, indexed := packs[packID]; !indexed {
			result.Findings = append(result.Findings, Finding{Type: FindingOrphanedPack, ID: packID.String()})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("repository: list packs for check: %w", err)
	}
	return nil
}

// checkBlobData reads, decrypts, decompresses, and hashes every indexed
// blob — reachable or not — comparing the plaintext hash against the blob
// id. Failures become corrupt_blob findings carrying the blob and its pack.
// Cancellation is checked between packs.
func (r *Repository) checkBlobData(ctx context.Context, result *CheckResult, packs map[incremental.ID]*[]index.Blob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	packIDs := make([]incremental.ID, 0, len(packs))
	for id := range packs {
		packIDs = append(packIDs, id)
	}
	sort.Slice(packIDs, func(i, j int) bool { return bytes.Compare(packIDs[i][:], packIDs[j][:]) < 0 })
	for _, packID := range packIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := incremental.Handle{Type: incremental.DataFile, Name: packID.String()}
		info, err := r.backend.Stat(ctx, h)
		if r.backend.IsNotExist(err) {
			continue // already reported by checkPacks
		}
		if err != nil {
			return fmt.Errorf("repository: stat pack %s: %w", packID, err)
		}
		blobs := *packs[packID]
		sort.Slice(blobs, func(i, j int) bool { return blobs[i].Offset < blobs[j].Offset })
		for _, blob := range blobs {
			if err := r.verifyBlob(ctx, h, info.Size, packID, blob); err != nil {
				result.Findings = append(result.Findings, Finding{Type: FindingCorruptBlob, ID: blob.ID.String(), PackID: packID.String(), Detail: err.Error()})
			}
		}
	}
	return nil
}

// verifyBlob authenticates one stored blob: read its ciphertext range,
// decrypt it, decompress when the index says compressed, and compare the
// SHA-256 of the plaintext with the blob id.
func (r *Repository) verifyBlob(ctx context.Context, h incremental.Handle, packSize int64, packID incremental.ID, blob index.Blob) error {
	if int64(blob.Offset)+int64(blob.Length) > packSize {
		return fmt.Errorf("blob extends past the end of its pack")
	}
	var sealed []byte
	err := r.backend.Load(ctx, h, int(blob.Length), int64(blob.Offset), func(rd io.Reader) error {
		var err error
		sealed, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return fmt.Errorf("blob could not be read from its pack")
	}
	plain, err := r.master.Open(nil, sealed)
	if err != nil {
		return fmt.Errorf("blob does not decrypt with the repository key")
	}
	if blob.UncompressedLength > 0 {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return fmt.Errorf("blob could not be decompressed")
		}
		defer decoder.Close()
		plain, err = decoder.DecodeAll(plain, nil)
		if err != nil {
			return fmt.Errorf("blob could not be decompressed")
		}
	}
	if incremental.Hash(plain) != blob.ID {
		return fmt.Errorf("blob content hash does not match its id")
	}
	return nil
}

// sortFindings orders findings by type, then object id, so check output is
// deterministic.
func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Type != findings[j].Type {
			return findings[i].Type < findings[j].Type
		}
		return findings[i].ID < findings[j].ID
	})
}
