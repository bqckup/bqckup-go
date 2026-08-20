package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/rand"
	"testing"
)

func testPol(t *testing.T) Pol {
	t.Helper()
	pol, err := RandomPolynomial()
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// chunkAll collects every chunk; boundaries are (Start, Length) pairs.
func chunkAll(t *testing.T, rd io.Reader, pol Pol) (sizes []int, total int) {
	t.Helper()
	c := New(rd, pol)
	var buf []byte
	for {
		chunk, err := c.Next(buf)
		if err == io.EOF {
			return sizes, total
		}
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, int(chunk.Length))
		total += int(chunk.Length)
		buf = chunk.Data
	}
}

func TestDeterministicBoundaries(t *testing.T) {
	pol := testPol(t)
	data := seededBytes(t, 8*miB)
	first, _ := chunkAll(t, bytes.NewReader(data), pol)
	second, _ := chunkAll(t, bytes.NewReader(data), pol)
	if len(first) != len(second) {
		t.Fatalf("chunk count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("chunk %d size differs: %d vs %d", i, first[i], second[i])
		}
	}
}

func TestChunkSizeBounds(t *testing.T) {
	pol := testPol(t)
	data := seededBytes(t, 32*miB)
	sizes, total := chunkAll(t, bytes.NewReader(data), pol)
	if total != len(data) {
		t.Fatalf("reconstructed %d bytes, want %d", total, len(data))
	}
	for i, size := range sizes {
		isLast := i == len(sizes)-1
		if size < MinSize {
			t.Fatalf("chunk %d is %d bytes, below min %d", i, size, MinSize)
		}
		if size > MaxSize && !isLast {
			t.Fatalf("chunk %d is %d bytes, above max %d", i, size, MaxSize)
		}
	}
}

func TestAverageChunkSize(t *testing.T) {
	pol := testPol(t)
	data := seededBytes(t, 64*miB)
	sizes, _ := chunkAll(t, bytes.NewReader(data), pol)
	avg := len(data) / len(sizes)
	// The split mask alone would give a 1 MiB mean, but splits below MinSize
	// are suppressed; memorylessness shifts the mean to MinSize + 2^20
	// = 1.5 MiB (same as upstream). Wide band to absorb polynomial variance.
	if avg < miB || avg > 2*miB {
		t.Fatalf("average chunk size %d out of tolerance [1MiB, 2MiB]", avg)
	}
}

func TestInsertChangesFewChunks(t *testing.T) {
	pol := testPol(t)
	data := seededBytes(t, 16*miB)
	inserted := append([]byte(nil), data[:len(data)/2]...)
	inserted = append(inserted, 0x00)
	inserted = append(inserted, data[len(data)/2:]...)

	type chunkInfo struct {
		size int
		hash [32]byte
	}
	collect := func(buf []byte) []chunkInfo {
		c := New(bytes.NewReader(buf), pol)
		var out []chunkInfo
		var reuse []byte
		for {
			chunk, err := c.Next(reuse)
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, chunkInfo{size: int(chunk.Length), hash: sha256.Sum256(chunk.Data)})
			reuse = chunk.Data
		}
	}
	before := collect(data)
	after := collect(inserted)

	different := 0
	for i := 0; i < len(before) || i < len(after); i++ {
		if i >= len(before) || i >= len(after) || before[i] != after[i] {
			different++
		}
	}
	// CDC: a 1-byte insert realigns only the chunks covering the insert
	// point (at most the touched chunk plus its neighbor).
	if different > 3 {
		t.Fatalf("1-byte insert changed %d chunks, want <= 3", different)
	}
}

func TestPolynomialDegree53Irreducible(t *testing.T) {
	for i := 0; i < 5; i++ {
		pol, err := RandomPolynomial()
		if err != nil {
			t.Fatal(err)
		}
		if pol.Deg() != 53 {
			t.Fatalf("degree = %d, want 53", pol.Deg())
		}
		if pol&(1<<53) == 0 {
			t.Fatal("bit 53 is not set")
		}
		if pol>>54 != 0 {
			t.Fatal("bits above 53 are not masked")
		}
		if !pol.Irreducible() {
			t.Fatalf("polynomial %x is not irreducible", uint64(pol))
		}
	}
}

func TestPolynomialJSONRoundTrip(t *testing.T) {
	pol := testPol(t)
	doc, err := json.Marshal(pol)
	if err != nil {
		t.Fatal(err)
	}
	var restored Pol
	if err := json.Unmarshal(doc, &restored); err != nil {
		t.Fatal(err)
	}
	if restored != pol {
		t.Fatal("polynomial JSON round trip mismatch")
	}
}

func TestEmptyReaderEOF(t *testing.T) {
	pol := testPol(t)
	c := New(bytes.NewReader(nil), pol)
	if _, err := c.Next(nil); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

// seededBytes returns deterministic pseudo-random data for stable tests.
func seededBytes(t *testing.T, n int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(42))
	data := make([]byte, n)
	if _, err := rng.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}
