package lock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTryLockWritesAndClearsOwnerMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := New(dir)
	unlock, acquired, err := locker.TryLock(context.Background(), "example")
	if err != nil || !acquired {
		t.Fatalf("TryLock() = acquired %v, err %v", acquired, err)
	}
	path := filepath.Join(dir, "example.lock")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatal("lock metadata is empty")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 0 {
		t.Fatalf("metadata remains after unlock: %q", contents)
	}
}

func TestInspectTreatsHeldLegacyLockAsUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "example.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)

	inspection, err := New(dir).Inspect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Held || !inspection.Unknown || inspection.Owner != nil {
		t.Fatalf("Inspect() = %+v, want held unknown legacy lock", inspection)
	}
}

func TestWaitReleasedTimesOutWithoutChangingHeldLock(t *testing.T) {
	t.Parallel()
	locker := New(t.TempDir())
	unlock, acquired, err := locker.TryLock(context.Background(), "example")
	if err != nil || !acquired {
		t.Fatalf("TryLock() = acquired %v, err %v", acquired, err)
	}
	defer unlock()
	if err := locker.WaitReleased(context.Background(), "example", 10*time.Millisecond); err == nil {
		t.Fatal("WaitReleased() succeeded while lock was held")
	}
	inspection, err := locker.Inspect(context.Background(), "example")
	if err != nil || !inspection.Held {
		t.Fatalf("Inspect() after timeout = %+v, %v", inspection, err)
	}
}

func TestInspectRecognizesHeldLockOwner(t *testing.T) {
	t.Parallel()
	locker := New(t.TempDir())
	unlock, acquired, err := locker.TryLock(context.Background(), "example")
	if err != nil || !acquired {
		t.Fatalf("TryLock() = acquired %v, err %v", acquired, err)
	}
	defer unlock()

	inspection, err := locker.Inspect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Held || inspection.Owner == nil || inspection.Unknown {
		t.Fatalf("Inspect() = %+v, want held valid owner", inspection)
	}
}
