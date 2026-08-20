package repository

import (
	"fmt"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/chunker"
)

// Config is the repository config document (verification notes §2.2):
// {"version":2,"id":"<hex>","chunker_polynomial":"<hex>"}. It is the only
// stored file that is not zstd-compressed.
type Config struct {
	Version           int         `json:"version"`
	ID                restic.ID   `json:"id"`
	ChunkerPolynomial chunker.Pol `json:"chunker_polynomial"`
}

// Validate checks the version range and the polynomial, mirroring what the
// official restic binary verifies when loading a config.
func (c Config) Validate() error {
	if c.Version != 2 {
		return fmt.Errorf("repository: unsupported repository version %d (this engine writes and reads version 2)", c.Version)
	}
	if c.ID.IsNull() {
		return fmt.Errorf("repository: config is missing its repository id")
	}
	if c.ChunkerPolynomial.Deg() != 53 {
		return fmt.Errorf("repository: chunker polynomial must have degree 53, got %d", c.ChunkerPolynomial.Deg())
	}
	if !c.ChunkerPolynomial.Irreducible() {
		return fmt.Errorf("repository: chunker polynomial is not irreducible")
	}
	return nil
}
