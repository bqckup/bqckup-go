package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
)

const rekeyNewPassword = "rotated-repository-password"

func countKeyFiles(t *testing.T, ctx context.Context, b backend.Backend) int {
	t.Helper()
	n := 0
	if err := b.List(ctx, incremental.KeyFileType, func(incremental.Handle, int64) error {
		n++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRekeyReplacesPassword(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	if countKeyFiles(t, ctx, local) != 1 {
		t.Fatal("expected exactly one key file after init")
	}

	result, err := Rekey(ctx, local, testPassword, rekeyNewPassword)
	if err != nil {
		t.Fatal(err)
	}
	if result.KeyFileAdded != 1 || result.KeyFilesRemoved != 1 {
		t.Fatalf("unexpected rekey result: %+v", result)
	}
	if countKeyFiles(t, ctx, local) != 1 {
		t.Fatalf("expected one key file after rekey, got %d", countKeyFiles(t, ctx, local))
	}

	reopened, err := Open(ctx, local, rekeyNewPassword)
	if err != nil {
		t.Fatalf("new password must open the repository: %v", err)
	}
	if *reopened.MasterKey() != *repo.MasterKey() {
		t.Fatal("rekey must keep the same master key")
	}
	if _, err := Open(ctx, local, testPassword); !errors.Is(err, incremental.ErrInvalidPassword) {
		t.Fatalf("old password must stop working, got %v", err)
	}
}

func TestRekeyKeepsDataReadable(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	id, err := repo.SaveBlob(ctx, incremental.DataBlob, []byte("payload before rekey"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := Rekey(ctx, local, testPassword, rekeyNewPassword); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, local, rekeyNewPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadBlob(ctx, incremental.DataBlob, id); err != nil {
		t.Fatalf("blob written before rekey must still load: %v", err)
	}
}

func TestRekeyWrongCurrentPasswordChangesNothing(t *testing.T) {
	ctx := context.Background()
	_, local := newRepo(t, ctx)

	if _, err := Rekey(ctx, local, "not-the-password", rekeyNewPassword); !errors.Is(err, incremental.ErrInvalidPassword) {
		t.Fatalf("expected invalid password, got %v", err)
	}
	if countKeyFiles(t, ctx, local) != 1 {
		t.Fatal("a failed rekey must not add or remove key files")
	}
	if _, err := Open(ctx, local, testPassword); err != nil {
		t.Fatalf("original password must keep working: %v", err)
	}
}

func TestRekeyRejectsSamePassword(t *testing.T) {
	ctx := context.Background()
	_, local := newRepo(t, ctx)
	if _, err := Rekey(ctx, local, testPassword, testPassword); err == nil {
		t.Fatal("rekey to the same password must be rejected")
	}
	if _, err := Rekey(ctx, local, testPassword, ""); err == nil {
		t.Fatal("empty new password must be rejected")
	}
	if countKeyFiles(t, ctx, local) != 1 {
		t.Fatal("rejected rekey must not change key files")
	}
}
