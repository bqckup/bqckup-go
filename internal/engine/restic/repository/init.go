package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/chunker"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
)

// Init creates a new repository, or opens it when it already exists
// (idempotent: existing data is never touched).
func Init(ctx context.Context, b backend.Backend, password string) (*Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if local, ok := b.(*backend.Local); ok {
		if err := local.CreateLayout(); err != nil {
			return nil, fmt.Errorf("repository: create layout: %w", err)
		}
	}
	// Already initialized: open instead of overwriting.
	if _, err := b.Stat(ctx, restic.Handle{Type: restic.ConfigFile}); err == nil {
		return Open(ctx, b, password)
	} else if !b.IsNotExist(err) {
		return nil, fmt.Errorf("repository: inspect config: %w", err)
	}

	master, err := crypto.NewRandomMasterKey()
	if err != nil {
		return nil, err
	}
	polynomial, err := chunker.RandomPolynomial()
	if err != nil {
		return nil, fmt.Errorf("repository: generate chunker polynomial: %w", err)
	}
	config := Config{Version: 2, ID: restic.Hash(master.Encrypt[:]), ChunkerPolynomial: polynomial}
	if err := saveConfig(ctx, b, master, config); err != nil {
		return nil, err
	}
	repo, err := newRepository(b, master, config)
	if err != nil {
		return nil, err
	}
	if err := repo.saveKeyFile(ctx, password); err != nil {
		return nil, err
	}
	return repo, nil
}

// Open loads an existing repository: unlocks a key file with the password,
// decrypts and validates the config with that key, and loads every index
// file into the master index.
func Open(ctx context.Context, b backend.Backend, password string) (*Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// a repository without a config does not exist (checked before touching
	// keys so a fresh directory reports ErrRepoNotFound, not a bad password)
	if _, err := b.Stat(ctx, restic.Handle{Type: restic.ConfigFile}); b.IsNotExist(err) {
		return nil, fmt.Errorf("%w: no config file found", restic.ErrRepoNotFound)
	}
	master, err := unlockKeyFile(ctx, b, password)
	if err != nil {
		return nil, err
	}
	config, err := loadConfig(ctx, b, master)
	if err != nil {
		return nil, err
	}
	repo, err := newRepository(b, master, config)
	if err != nil {
		return nil, err
	}
	if err := repo.index.LoadAll(ctx, b, master); err != nil {
		return nil, fmt.Errorf("repository: load indexes: %w", err)
	}
	return repo, nil
}

// saveConfig seals and stores the config document under the empty-name
// handle ("config" at the repository root).
func saveConfig(ctx context.Context, b backend.Backend, master *crypto.MasterKey, config Config) error {
	doc, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("repository: marshal config: %w", err)
	}
	sealed, err := master.Seal(nil, doc)
	if err != nil {
		return err
	}
	return b.Save(ctx, restic.Handle{Type: restic.ConfigFile}, bytes.NewReader(sealed))
}

// loadConfig reads and decrypts the config document. A missing config is
// ErrRepoNotFound; a config the key cannot open is corrupted (redacted).
func loadConfig(ctx context.Context, b backend.Backend, master *crypto.MasterKey) (Config, error) {
	if _, err := b.Stat(ctx, restic.Handle{Type: restic.ConfigFile}); b.IsNotExist(err) {
		return Config{}, fmt.Errorf("%w: no config file found", restic.ErrRepoNotFound)
	}
	var raw []byte
	err := b.Load(ctx, restic.Handle{Type: restic.ConfigFile}, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return Config{}, fmt.Errorf("repository: load config: %w", err)
	}
	plain, err := master.Open(nil, raw)
	if err != nil {
		return Config{}, &restic.RedactedError{
			Category: "repository",
			Message:  "could not decrypt the repository config",
			Err:      err,
		}
	}
	var config Config
	if err := json.Unmarshal(plain, &config); err != nil {
		return Config{}, fmt.Errorf("repository: parse config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// saveKeyFile writes the encrypted key file. The file name is the SHA-256
// of the file bytes, exactly like restic (verification notes §2.4).
func (r *Repository) saveKeyFile(ctx context.Context, password string) error {
	username, hostname := CurrentIdentity()
	keyFile, err := crypto.NewKeyFile(password, username, hostname, r.master, time.Now())
	if err != nil {
		return err
	}
	doc, err := json.Marshal(keyFile)
	if err != nil {
		return fmt.Errorf("repository: marshal key file: %w", err)
	}
	name := restic.Hash(doc).String()
	return r.backend.Save(ctx, restic.Handle{Type: restic.KeyFileType, Name: name}, bytes.NewReader(doc))
}

// unlockKeyFile tries every key file in the repository with the password.
// The first key that opens wins; if none opens, the password is wrong.
func unlockKeyFile(ctx context.Context, b backend.Backend, password string) (*crypto.MasterKey, error) {
	var handles []restic.Handle
	if err := b.List(ctx, restic.KeyFileType, func(h restic.Handle, size int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("repository: list key files: %w", err)
	}
	if len(handles) == 0 {
		return nil, fmt.Errorf("%w: repository contains no key files", restic.ErrInvalidPassword)
	}
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
			continue // not a key file we understand
		}
		master, err := keyFile.MasterKey(password)
		if err == nil {
			return master, nil
		}
	}
	// No key opened: the password is wrong. No secret appears in the message.
	return nil, fmt.Errorf("%w: no key file matched the given password", restic.ErrInvalidPassword)
}

// CurrentIdentity returns the OS user and host name, or "unknown" for
// either when it cannot be determined.
func CurrentIdentity() (username, hostname string) {
	username = "unknown"
	if current, err := user.Current(); err == nil && current.Username != "" {
		username = current.Username
	}
	hostname = "unknown"
	if name, err := os.Hostname(); err == nil && name != "" {
		hostname = name
	}
	return username, hostname
}
