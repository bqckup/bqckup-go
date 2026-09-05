package pack

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
)

func testMaster(t *testing.T) *crypto.MasterKey {
	t.Helper()
	key, err := crypto.NewRandomMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// addBlob seals fake plaintext and adds it to the builder like the
// repository layer would.
func addBlob(t *testing.T, b *Builder, master *crypto.MasterKey, blobType incremental.BlobType, plaintext []byte) {
	t.Helper()
	sealed, err := master.Seal(nil, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	uncompressedLength := uint32(0)
	if blobType == incremental.CompressedDataBlob || blobType == incremental.CompressedTreeBlob {
		uncompressedLength = uint32(len(plaintext))
	}
	b.Add(blobType, incremental.Hash(plaintext), sealed, uncompressedLength)
}

func TestBuildParseRoundTrip(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	plaintexts := [][]byte{
		[]byte("first data blob"),
		bytes.Repeat([]byte{0x42}, 1024),
		[]byte(`{"nodes":[]}`),
	}
	types := []incremental.BlobType{incremental.DataBlob, incremental.CompressedDataBlob, incremental.TreeBlob}
	for i := range plaintexts {
		addBlob(t, b, master, types[i], plaintexts[i])
	}
	packData, err := b.Finalize(master)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := Parse(packData, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 3 {
		t.Fatalf("parsed %d blobs, want 3", len(blobs))
	}
	wantOffset := uint32(0)
	for i, blob := range blobs {
		if blob.Type != types[i] {
			t.Fatalf("blob %d type = %d, want %d", i, blob.Type, types[i])
		}
		if blob.ID != incremental.Hash(plaintexts[i]) {
			t.Fatalf("blob %d id mismatch", i)
		}
		if blob.Offset != wantOffset {
			t.Fatalf("blob %d offset = %d, want %d", i, blob.Offset, wantOffset)
		}
		wantOffset += blob.Length
		if types[i] == incremental.CompressedDataBlob && blob.UncompressedLength != uint32(len(plaintexts[i])) {
			t.Fatalf("blob %d uncompressed length = %d, want %d", i, blob.UncompressedLength, len(plaintexts[i]))
		}
	}
	// every blob payload must decrypt back to the plaintext
	for i, blob := range blobs {
		start := int(blob.Offset)
		opened, err := master.Open(nil, packData[start:start+int(blob.Length)])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(opened, plaintexts[i]) {
			t.Fatalf("blob %d payload mismatch", i)
		}
	}
}

func TestHeaderTrailerValidated(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	addBlob(t, b, master, incremental.DataBlob, []byte("payload"))
	packData, err := b.Finalize(master)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), packData...)
	corrupted[len(corrupted)-1] ^= 0xff // corrupt the trailer
	if _, err := Parse(corrupted, master); err == nil {
		t.Fatal("want error for corrupted trailer")
	}
}

func TestCorruptedHeaderRejected(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	addBlob(t, b, master, incremental.DataBlob, bytes.Repeat([]byte{7}, 4096))
	packData, err := b.Finalize(master)
	if err != nil {
		t.Fatal(err)
	}
	// flip one byte inside the sealed header (the MAC must catch it)
	headerStart := len(packData) - trailerSize - (crypto.Extension + entryUncompressed)
	packData[headerStart+2] ^= 0x01
	if _, err := Parse(packData, master); err == nil {
		t.Fatal("want error for corrupted header")
	}
}

func TestEmptyPackRejected(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	if _, err := b.Finalize(master); err == nil {
		t.Fatal("want error finalizing an empty pack")
	}
	// a header with zero entries must also be rejected by the parser
	emptyHeader, err := master.Seal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	crafted := append(emptyHeader,
		byte(len(emptyHeader)), byte(len(emptyHeader)>>8), byte(len(emptyHeader)>>16), byte(len(emptyHeader)>>24))
	if _, err := Parse(crafted, master); err == nil {
		t.Fatal("want error parsing a pack with an empty header")
	}
}

func TestTruncatedPackRejected(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	addBlob(t, b, master, incremental.DataBlob, []byte("payload"))
	packData, err := b.Finalize(master)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{0, 3, len(packData) / 2, len(packData) - 1} {
		if _, err := Parse(packData[:cut], master); err == nil {
			t.Fatalf("want error for pack truncated to %d bytes", cut)
		}
	}
}

func TestLargeBlobRoundTrip(t *testing.T) {
	master := testMaster(t)
	b := NewBuilder()
	big := make([]byte, 8*1024*1024) // one max-size chunk
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	addBlob(t, b, master, incremental.CompressedTreeBlob, big)
	packData, err := b.Finalize(master)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := Parse(packData, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].UncompressedLength != uint32(len(big)) {
		t.Fatalf("unexpected parse result: %+v", blobs)
	}
}
