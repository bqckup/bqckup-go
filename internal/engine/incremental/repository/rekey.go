package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
)

// RekeyResult reports the key-file changes made by Rekey.
type RekeyResult struct {
	KeyFileAdded    int
	KeyFilesRemoved int
}

// Rekey replaces the repository password without touching the data.
//
// The repository master key does not change, only the key files that wrap
// it. Rekey therefore runs in three steps:
//
//  1. Add a key file wrapping the master key with newPassword, then check
//     that newPassword opens the repository to the same master key.
//  2. Only after that check passes, remove every key file that opened with
//     oldPassword.
//  3. Anything that fails before step 2 leaves the old key files in place,
//     so the old password keeps working.
func Rekey(ctx context.Context, b backend.Backend, oldPassword, newPassword string) (RekeyResult, error) {
	if err := ctx.Err(); err != nil {
		return RekeyResult{}, err
	}
	if oldPassword == "" || newPassword == "" {
		return RekeyResult{}, errors.New("repository: passwords must not be empty")
	}
	if oldPassword == newPassword {
		return RekeyResult{}, errors.New("repository: new password must differ from the current password")
	}

	keyFiles, err := loadKeyFiles(ctx, b)
	if err != nil {
		return RekeyResult{}, err
	}
	var master *crypto.MasterKey
	var oldHandles []incremental.Handle
	for _, kf := range keyFiles {
		opened, err := kf.keyFile.MasterKey(oldPassword)
		if err != nil {
			continue
		}
		if master == nil {
			master = opened
		}
		oldHandles = append(oldHandles, kf.handle)
	}
	if master == nil {
		return RekeyResult{}, fmt.Errorf("%w: no key file opens with the current password", incremental.ErrInvalidPassword)
	}
	for _, kf := range keyFiles {
		if _, err := kf.keyFile.MasterKey(newPassword); err == nil {
			return RekeyResult{}, errors.New("repository: new password already opens this repository")
		}
	}

	result := RekeyResult{}
	if err := saveKeyFileFor(ctx, b, master, newPassword); err != nil {
		return result, err
	}
	result.KeyFileAdded = 1

	verified, err := unlockKeyFile(ctx, b, newPassword)
	if err != nil {
		return result, fmt.Errorf("repository: verify new password: %w; old key files kept", err)
	}
	if *verified != *master {
		return result, errors.New("repository: new key file opens a different master key; old key files kept")
	}

	for _, h := range oldHandles {
		if err := b.Remove(ctx, h); err != nil {
			return result, fmt.Errorf("repository: remove old key file: %w", err)
		}
		result.KeyFilesRemoved++
	}
	return result, nil
}

// keyFileEntry is one parseable key file found in the repository.
type keyFileEntry struct {
	handle  incremental.Handle
	keyFile crypto.KeyFile
}

// loadKeyFiles reads every key file in the repository that parses as one.
func loadKeyFiles(ctx context.Context, b backend.Backend) ([]keyFileEntry, error) {
	var handles []incremental.Handle
	if err := b.List(ctx, incremental.KeyFileType, func(h incremental.Handle, _ int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("repository: list key files: %w", err)
	}
	var entries []keyFileEntry
	for _, h := range handles {
		var doc []byte
		err := b.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			var err error
			doc, err = io.ReadAll(rd)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("repository: load key file: %w", err)
		}
		var keyFile crypto.KeyFile
		if err := json.Unmarshal(doc, &keyFile); err != nil {
			continue
		}
		entries = append(entries, keyFileEntry{handle: h, keyFile: keyFile})
	}
	return entries, nil
}

// saveKeyFileFor writes a key file that wraps master with password, using
// the same naming and encoding as saveKeyFile.
func saveKeyFileFor(ctx context.Context, b backend.Backend, master *crypto.MasterKey, password string) error {
	username, hostname := CurrentIdentity()
	keyFile, err := crypto.NewKeyFile(password, username, hostname, master, time.Now())
	if err != nil {
		return err
	}
	doc, err := json.Marshal(keyFile)
	if err != nil {
		return fmt.Errorf("repository: marshal key file: %w", err)
	}
	handle := incremental.Handle{Type: incremental.KeyFileType, Name: incremental.Hash(doc).String()}
	return b.Save(ctx, handle, bytes.NewReader(doc))
}
