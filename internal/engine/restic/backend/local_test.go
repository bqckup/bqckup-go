package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

func dataHandle(name string) restic.Handle {
	return restic.Handle{Type: restic.DataFile, Name: name}
}

func longName(seed byte) string {
	name := make([]byte, 64)
	for i := range name {
		name[i] = 'a' + seed%16
	}
	return string(name)
}

func TestSaveLoadRoundTripAllTypes(t *testing.T) {
	b := NewLocal(t.TempDir())
	payload := []byte("round trip payload for every file type")
	handles := map[restic.FileType]restic.Handle{
		restic.ConfigFile:   {Type: restic.ConfigFile},
		restic.KeyFileType:  {Type: restic.KeyFileType, Name: longName(1)},
		restic.LockFile:     {Type: restic.LockFile, Name: longName(2)},
		restic.SnapshotFile: {Type: restic.SnapshotFile, Name: longName(3)},
		restic.IndexFile:    {Type: restic.IndexFile, Name: longName(4)},
		restic.DataFile:     dataHandle(longName(5)),
	}
	for _, h := range handles {
		t.Run(string(h.Type), func(t *testing.T) {
			if err := b.Save(context.Background(), h, bytes.NewReader(payload)); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			err := b.Load(context.Background(), h, 0, 0, func(rd io.Reader) error {
				_, err := io.Copy(&got, rd)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), payload) {
				t.Fatal("load/save mismatch")
			}
			info, err := b.Stat(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size != int64(len(payload)) {
				t.Fatalf("stat size = %d, want %d", info.Size, len(payload))
			}
		})
	}
}

func TestLoadOffsetAndLength(t *testing.T) {
	b := NewLocal(t.TempDir())
	h := dataHandle(longName(1))
	payload := []byte("0123456789abcdef")
	if err := b.Save(context.Background(), h, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	err := b.Load(context.Background(), h, 6, 3, func(rd io.Reader) error {
		_, err := io.Copy(&got, rd)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "345678" {
		t.Fatalf("sliced load = %q, want %q", got.String(), "345678")
	}
}

func TestListAndRemove(t *testing.T) {
	b := NewLocal(t.TempDir())
	ctx := context.Background()
	keys := []string{longName(1), longName(2), longName(3)}
	for _, name := range keys {
		h := restic.Handle{Type: restic.KeyFileType, Name: name}
		if err := b.Save(ctx, h, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	listed := map[string]int64{}
	err := b.List(ctx, restic.KeyFileType, func(h restic.Handle, size int64) error {
		listed[h.Name] = size
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(keys) {
		t.Fatalf("listed %d keys, want %d", len(listed), len(keys))
	}
	for _, name := range keys {
		if size, ok := listed[name]; !ok || size != 1 {
			t.Fatalf("key %s missing or wrong size: %v", name, listed)
		}
	}

	removed := restic.Handle{Type: restic.KeyFileType, Name: keys[0]}
	if err := b.Remove(ctx, removed); err != nil {
		t.Fatal(err)
	}
	// removing again must not error
	if err := b.Remove(ctx, removed); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Stat(ctx, removed); !b.IsNotExist(err) {
		t.Fatalf("removed key still statable: %v", err)
	}
}

func TestListMissingDirsIsEmpty(t *testing.T) {
	b := NewLocal(t.TempDir())
	count := 0
	if err := b.List(context.Background(), restic.DataFile, func(restic.Handle, int64) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("listed %d files in empty repo", count)
	}
}

func TestFailedSaveLeavesNothingBehind(t *testing.T) {
	b := NewLocal(t.TempDir())
	h := dataHandle(longName(1))
	boom := errors.New("boom mid-stream")
	err := b.Save(context.Background(), h, &errorReader{err: boom, after: 1024})
	if !errors.Is(err, boom) {
		t.Fatalf("want %v, got %v", boom, err)
	}
	if _, err := b.Stat(context.Background(), h); !b.IsNotExist(err) {
		t.Fatalf("partial file exists at final path: %v", err)
	}
	// no leftover staging files
	tmpEntries, err := os.ReadDir(filepath.Join(b.layout.Dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpEntries) != 0 {
		t.Fatalf("leftover tmp files: %d", len(tmpEntries))
	}
}

func TestCancelledSaveRemovesTmpFile(t *testing.T) {
	b := NewLocal(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	h := dataHandle(longName(1))
	reader := &blockingReader{ctx: ctx, started: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Save(ctx, h, reader)
	}()
	<-reader.started // wait until Save is mid-write
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if _, err := b.Stat(context.Background(), h); !b.IsNotExist(err) {
		t.Fatalf("partial file exists at final path: %v", err)
	}
	tmpEntries, err := os.ReadDir(filepath.Join(b.layout.Dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpEntries) != 0 {
		t.Fatalf("leftover tmp files after cancellation: %d", len(tmpEntries))
	}
}

func TestPermissions(t *testing.T) {
	b := NewLocal(t.TempDir())
	if err := b.CreateLayout(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		info, err := os.Stat(filepath.Join(b.layout.Dir, "data", fmt.Sprintf("%02x", i)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("data/%02x mode = %o, want 700", i, info.Mode().Perm())
		}
	}
	for _, dir := range []string{"keys", "index", "snapshots", "locks", "tmp"} {
		info, err := os.Stat(filepath.Join(b.layout.Dir, dir))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 700", dir, info.Mode().Perm())
		}
	}

	h := dataHandle(longName(2))
	if err := b.Save(context.Background(), h, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	path, err := b.layout.Path(h)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestInvalidHandlesRejected(t *testing.T) {
	b := NewLocal(t.TempDir())
	if err := b.Save(context.Background(), dataHandle("short"), strings.NewReader("x")); err == nil {
		t.Fatal("want error for short data name")
	}
	if err := b.Save(context.Background(), restic.Handle{Type: restic.FileType("bogus"), Name: "x"}, strings.NewReader("x")); err == nil {
		t.Fatal("want error for unknown file type")
	}
	if err := b.Save(context.Background(), restic.Handle{Type: restic.KeyFileType}, strings.NewReader("x")); err == nil {
		t.Fatal("want error for empty key name")
	}
}

// errorReader yields after bytes then fails with err.
type errorReader struct {
	err   error
	after int
}

func (r *errorReader) Read(p []byte) (int, error) {
	if r.after <= 0 {
		return 0, r.err
	}
	if len(p) > r.after {
		p = p[:r.after]
	}
	r.after -= len(p)
	return len(p), nil
}

// blockingReader yields one buffer then blocks until the context is done.
type blockingReader struct {
	ctx     context.Context
	once    bool
	started chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if !r.once {
		r.once = true
		close(r.started)
		for i := range p {
			p[i] = 0x5a
		}
		return len(p), nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
