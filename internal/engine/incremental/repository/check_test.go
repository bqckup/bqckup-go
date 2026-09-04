package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/index"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/tree"
)

// seedCheckFixture builds a healthy repository with two data blobs that a
// tree references (alpha, beta), one data blob no tree references (gamma),
// a root tree with a subtree, and one snapshot. All data blobs share one
// pack; both trees share a second pack; each flush produced its own index
// file. indexA/indexB name the index handles, matched by content.
type seedCheckFixture struct {
	local         *backend.Local
	alpha, beta   incremental.ID
	gamma         incremental.ID // unreferenced on purpose
	root, sub     incremental.ID
	snapID        incremental.ID
	dataPack      incremental.ID
	treePack      incremental.ID
	dataEntries   []index.Blob
	treeEntries   []index.Blob
	dataIndexH    incremental.Handle
	treeIndexH    incremental.Handle
	dataPackBytes []byte
	treePackBytes []byte
}

func seedCheck(t *testing.T, ctx context.Context) *seedCheckFixture {
	t.Helper()
	repo, local := newRepo(t, ctx)
	fx := &seedCheckFixture{local: local}
	var err error
	if fx.alpha, err = repo.SaveBlob(ctx, incremental.DataBlob, []byte("alpha content")); err != nil {
		t.Fatal(err)
	}
	if fx.beta, err = repo.SaveBlob(ctx, incremental.DataBlob, []byte("beta content bytes")); err != nil {
		t.Fatal(err)
	}
	if fx.gamma, err = repo.SaveBlob(ctx, incremental.DataBlob, []byte("gamma sits in the pack unreferenced")); err != nil {
		t.Fatal(err)
	}
	subDoc, err := (&tree.Tree{Nodes: []*tree.Node{
		{Name: "beta.txt", Type: tree.TypeFile, Mode: 0o644, Content: []incremental.ID{fx.beta}},
	}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if fx.sub, err = repo.SaveBlob(ctx, incremental.TreeBlob, subDoc); err != nil {
		t.Fatal(err)
	}
	// marshal after fx.sub is set: the node holds &fx.sub, resolved at marshal time
	rootDoc, err := (&tree.Tree{Nodes: []*tree.Node{
		{Name: "alpha.txt", Type: tree.TypeFile, Mode: 0o644, Content: []incremental.ID{fx.alpha}},
		{Name: "sub", Type: tree.TypeDir, Mode: 0o755, Subtree: &fx.sub},
	}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if fx.root, err = repo.SaveBlob(ctx, incremental.TreeBlob, rootDoc); err != nil {
		t.Fatal(err)
	}
	snap := snapshot.Snapshot{
		Time:     time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
		Hostname: "host-a",
		Tree:     &fx.root,
		Paths:    []string{"/srv/www"},
		Tags:     []string{"site:test"},
	}
	if fx.snapID, err = repo.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	var handles []incremental.Handle
	if err := local.List(ctx, incremental.IndexFile, func(h incremental.Handle, _ int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, h := range handles {
		raw := readHandle(t, ctx, local, h)
		doc, err := index.Open(raw, repo.master)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range doc.Packs {
			if len(p.Blobs) > 0 && p.Blobs[0].Type == incremental.DataBlob {
				fx.dataIndexH = h
				fx.dataPack = p.ID
				fx.dataEntries = p.Blobs
			} else {
				fx.treeIndexH = h
				fx.treePack = p.ID
				fx.treeEntries = p.Blobs
			}
		}
	}
	fx.dataPackBytes = readHandle(t, ctx, local, incremental.Handle{Type: incremental.DataFile, Name: fx.dataPack.String()})
	fx.treePackBytes = readHandle(t, ctx, local, incremental.Handle{Type: incremental.DataFile, Name: fx.treePack.String()})
	return fx
}

func readHandle(t *testing.T, ctx context.Context, local *backend.Local, h incremental.Handle) []byte {
	t.Helper()
	var raw []byte
	if err := local.Load(ctx, h, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeHandle(t *testing.T, ctx context.Context, local *backend.Local, h incremental.Handle, raw []byte) {
	t.Helper()
	if err := local.Save(ctx, h, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func removeHandle(t *testing.T, ctx context.Context, local *backend.Local, h incremental.Handle) {
	t.Helper()
	if err := local.Remove(ctx, h); err != nil {
		t.Fatal(err)
	}
}

func xorByte(raw []byte, at int) {
	if at < 0 || at >= len(raw) {
		panic("corruption offset out of range")
	}
	raw[at] ^= 0xff
}

func corruptIndexFile(t *testing.T, fx *seedCheckFixture, target incremental.Handle, ctx context.Context) {
	t.Helper()
	raw := readHandle(t, ctx, fx.local, target)
	xorByte(raw, 8)
	writeHandle(t, ctx, fx.local, target, raw)
}

// runCheck opens tolerantly and runs the full check (without lock).
func runCheck(t *testing.T, ctx context.Context, fx *seedCheckFixture, password string, readData bool) (CheckResult, *Repository) {
	t.Helper()
	repo, findings, err := CheckOpen(ctx, fx.local, password)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CheckRepository(ctx, repo, findings, readData)
	if err != nil {
		t.Fatal(err)
	}
	return result, repo
}

func hasFinding(t *testing.T, result CheckResult, want Finding) {
	t.Helper()
	for _, got := range result.Findings {
		if got.Type == want.Type && got.ID == want.ID {
			return
		}
	}
	t.Fatalf("want finding %s %q in %+v", want.Type, want.ID, result.Findings)
}

func TestCheckHealthyRepository(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	result, repo := runCheck(t, ctx, fx, testPassword, false)
	if len(result.Findings) != 0 {
		t.Fatalf("healthy repository reported findings: %+v", result.Findings)
	}
	if repo == nil {
		t.Fatal("CheckOpen returned no repository")
	}
	if result.Indexes != 2 || result.Snapshots != 1 || result.Packs != 2 || result.Blobs != 5 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	// read-data mode stays clean on a healthy repository too
	result, _ = runCheck(t, ctx, fx, testPassword, true)
	if len(result.Findings) != 0 {
		t.Fatalf("read-data check reported findings: %+v", result.Findings)
	}
}

func TestCheckBrokenKeyFileBecomesFinding(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	// an extra key file whose content is not key JSON
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.KeyFileType, Name: string(bytes.Repeat([]byte("ab"), 32))}, []byte("not a key file"))
	repo, findings, err := CheckOpen(ctx, fx.local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if repo == nil {
		t.Fatal("the good key must still open the repository")
	}
	if len(findings) != 1 || findings[0].Type != FindingBrokenKey {
		t.Fatalf("want one broken_key finding, got %+v", findings)
	}
}

func TestCheckWrongPasswordLeavesNilRepositoryWithKeyFindings(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	repo, findings, err := CheckOpen(ctx, fx.local, "wrong-password")
	if err != nil {
		t.Fatal(err)
	}
	if repo != nil {
		t.Fatal("wrong password must not open a repository")
	}
	if len(findings) != 1 || findings[0].Type != FindingBrokenKey {
		t.Fatalf("want one broken_key finding, got %+v", findings)
	}
	result, err := CheckRepository(ctx, nil, findings, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected only the key findings, got %+v", result.Findings)
	}
}

func TestCheckCorruptedConfigBecomesBrokenConfig(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	raw := readHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.ConfigFile})
	xorByte(raw, 5)
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.ConfigFile}, raw)
	result, repo := runCheck(t, ctx, fx, testPassword, false)
	if repo == nil {
		t.Fatal("a broken config must not hide the healthy structure behind it")
	}
	hasFinding(t, result, Finding{Type: FindingBrokenConfig, ID: "config"})
}

// seedConfigMismatch rewrites the config sealed with a valid structure but
// an id that does not match the master key hash.
func TestCheckConfigIDMismatchBecomesBrokenConfig(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	repo, _, err := CheckOpen(ctx, fx.local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(repo.config)
	if err != nil {
		t.Fatal(err)
	}
	doc = bytes.Replace(doc, []byte(`"id":"`+repo.config.ID.String()+`"`), []byte(`"id":"`+incremental.Hash([]byte("other")).String()+`"`), 1)
	sealed, err := repo.master.Seal(nil, doc)
	if err != nil {
		t.Fatal(err)
	}
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.ConfigFile}, sealed)
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingBrokenConfig, ID: "config"})
}

func TestCheckBrokenIndexContinuesWithHealthyIndexes(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	corruptIndexFile(t, fx, fx.dataIndexH, ctx)
	result, repo := runCheck(t, ctx, fx, testPassword, false)
	if repo == nil {
		t.Fatal("healthy indexes must keep the repository usable")
	}
	hasFinding(t, result, Finding{Type: FindingBrokenIndex, ID: fx.dataIndexH.Name})
	// the healthy index still loaded: its pack is examined and its blobs counted
	if result.Indexes != 2 {
		t.Fatalf("both index files examined, got %d", result.Indexes)
	}
	if result.Packs != 1 {
		t.Fatalf("only the healthy pack is indexed, got %d", result.Packs)
	}
	// the data blobs the tree references are now unreferenced by any index
	hasFinding(t, result, Finding{Type: FindingMissingBlob, ID: fx.alpha.String(), SnapshotID: fx.snapID.String()})
}

func TestCheckNilTreeSnapshotBecomesBrokenSnapshot(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	repo, _, err := CheckOpen(ctx, fx.local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.SaveSnapshot(ctx, snapshot.Snapshot{
		Time:     time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		Hostname: "host-a",
		Tree:     nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingBrokenSnapshot, ID: id.String()})
}

func TestCheckGarbageSnapshotBecomesBrokenSnapshot(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.SnapshotFile, Name: string(bytes.Repeat([]byte("cd"), 32))}, []byte("garbage snapshot"))
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingBrokenSnapshot, ID: string(bytes.Repeat([]byte("cd"), 32))})
}

func TestCheckDeletedPackBecomesMissingPack(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	removeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: fx.dataPack.String()})
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	found := false
	for _, f := range result.Findings {
		if f.Type == FindingMissingPack && f.ID == fx.dataPack.String() {
			found = true
			if f.BlobCount != 3 {
				t.Fatalf("missing pack carries %d blobs, want 3", f.BlobCount)
			}
		}
	}
	if !found {
		t.Fatalf("want missing_pack %s in %+v", fx.dataPack, result.Findings)
	}
	// read-data must not error on the missing pack either
	runCheck(t, ctx, fx, testPassword, true)
}

func TestCheckOrphanedPackFailsCheck(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	orphan := incremental.Hash([]byte("orphan pack bytes"))
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: orphan.String()}, bytes.Repeat([]byte{9}, 64))
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingOrphanedPack, ID: orphan.String()})
}

func TestCheckNonPackNamesInDataAreIgnored(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	// the backend layout rejects non-hex names, but a foreign file placed
	// directly in the data area must be ignored like prune ignores it
	path := fx.local.DirPath() + "/data/aa/not-an-id"
	if err := os.WriteFile(path, []byte("leave me alone"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	if len(result.Findings) != 0 {
		t.Fatalf("a non-pack file must not be a finding: %+v", result.Findings)
	}
}

func TestCheckCorruptedPackHeaderBecomesBrokenPack(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	raw := append([]byte(nil), fx.dataPackBytes...)
	headerLen := int(raw[len(raw)-4]) | int(raw[len(raw)-3])<<8 | int(raw[len(raw)-2])<<16 | int(raw[len(raw)-1])<<24
	xorByte(raw, len(raw)-4-headerLen+2)
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: fx.dataPack.String()}, raw)
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingBrokenPack, ID: fx.dataPack.String()})
}

func TestCheckCorruptTreeBlobBecomesCorruptBlobWithSnapshot(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	var target *index.Blob
	for i := range fx.treeEntries {
		if fx.treeEntries[i].ID == fx.root {
			target = &fx.treeEntries[i]
			break
		}
	}
	if target == nil {
		t.Fatal("root tree entry not found")
	}
	raw := append([]byte(nil), fx.treePackBytes...)
	xorByte(raw, int(target.Offset)+5)
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: fx.treePack.String()}, raw)
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingCorruptBlob, ID: fx.root.String(), SnapshotID: fx.snapID.String()})
}

// TestCheckReadDataFindsPayloadCorruption shows the default check never
// reads blob payloads: a corrupted data blob payload surfaces only with
// read-data, and unreferenced blobs are verified too.
func TestCheckReadDataFindsPayloadCorruption(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	entryFor := func(blobID incremental.ID) index.Blob {
		t.Helper()
		for _, e := range fx.dataEntries {
			if e.ID == blobID {
				return e
			}
		}
		t.Fatal("blob not found in data pack")
		return index.Blob{}
	}
	// corrupt the payloads of the reachable alpha and the unreferenced
	// gamma in one write: their bytes are ciphertext, so any single-byte
	// flip inside the sealed range breaks decryption
	raw := append([]byte(nil), fx.dataPackBytes...)
	xorByte(raw, int(entryFor(fx.alpha).Offset)+5)
	xorByte(raw, int(entryFor(fx.gamma).Offset)+5)
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: fx.dataPack.String()}, raw)

	defaultResult, _ := runCheck(t, ctx, fx, testPassword, false)
	for _, f := range defaultResult.Findings {
		if f.Type == FindingCorruptBlob {
			t.Fatalf("default check read a payload and found %+v", f)
		}
	}
	readResult, _ := runCheck(t, ctx, fx, testPassword, true)
	hasFinding(t, readResult, Finding{Type: FindingCorruptBlob, ID: fx.alpha.String(), PackID: fx.dataPack.String()})
	hasFinding(t, readResult, Finding{Type: FindingCorruptBlob, ID: fx.gamma.String(), PackID: fx.dataPack.String()})
}

func TestCheckMissingBlobFromDroppedIndexEntry(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	// rewrite the data index without the alpha entry (pack stays stored)
	repo, _, err := CheckOpen(ctx, fx.local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	replacement := index.Index{}
	for _, stored := range repo.checkIndexDocs {
		for _, p := range stored.Packs {
			if p.ID != fx.dataPack {
				replacement.Packs = append(replacement.Packs, p)
				continue
			}
			kept := index.Pack{ID: p.ID}
			for _, blob := range p.Blobs {
				if blob.ID != fx.alpha {
					kept.Blobs = append(kept.Blobs, blob)
				}
			}
			replacement.Packs = append(replacement.Packs, kept)
		}
	}
	sealed, err := index.Seal(replacement, repo.master)
	if err != nil {
		t.Fatal(err)
	}
	name := incremental.Hash(sealed).String()
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.IndexFile, Name: name}, sealed)
	removeHandle(t, ctx, fx.local, fx.dataIndexH)

	result, _ := runCheck(t, ctx, fx, testPassword, false)
	hasFinding(t, result, Finding{Type: FindingMissingBlob, ID: fx.alpha.String(), SnapshotID: fx.snapID.String()})
}

func TestCheckFindingsSortedByTypeThenID(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	// several finding types at once
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.KeyFileType, Name: string(bytes.Repeat([]byte("ab"), 32))}, []byte("not a key"))
	corruptIndexFile(t, fx, fx.dataIndexH, ctx)
	orphan := incremental.Hash([]byte("orphan"))
	writeHandle(t, ctx, fx.local, incremental.Handle{Type: incremental.DataFile, Name: orphan.String()}, []byte("orphan bytes"))
	result, _ := runCheck(t, ctx, fx, testPassword, false)
	for i := 1; i < len(result.Findings); i++ {
		prev, cur := result.Findings[i-1], result.Findings[i]
		if prev.Type > cur.Type || (prev.Type == cur.Type && prev.ID > cur.ID) {
			t.Fatalf("findings not sorted at %d: %+v before %+v", i, prev, cur)
		}
	}
}

// writeTrackingBackend fails any Save or Remove: the check is read-only.
type writeTrackingBackend struct {
	backend.Backend
	writes int
}

func (b *writeTrackingBackend) Save(ctx context.Context, h incremental.Handle, rd io.Reader) error {
	b.writes++
	return nil
}

func (b *writeTrackingBackend) Remove(ctx context.Context, h incremental.Handle) error {
	b.writes++
	return nil
}

func TestCheckNeverWritesToBackend(t *testing.T) {
	ctx := context.Background()
	fx := seedCheck(t, ctx)
	tracked := &writeTrackingBackend{Backend: fx.local}
	repo, findings, err := CheckOpen(ctx, tracked, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if repo == nil {
		t.Fatal("no repository opened")
	}
	result, err := CheckRepository(ctx, repo, findings, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("tracked healthy repository must stay healthy: %+v", result.Findings)
	}
	if tracked.writes != 0 {
		t.Fatalf("check wrote %d times", tracked.writes)
	}
}
