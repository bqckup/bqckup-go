// Package restic is a pure-Go implementation of the Restic repository
// format v2 (L1: repository init, file backup, snapshot listing on local
// filesystems). Repositories written here are readable by the official
// restic binary >= 0.17.0 (restic check / snapshots / restore).
//
// The format contract is documented in
// docs/superpowers/notes/restic-format-verification.md, which was verified
// against the official restic source and wins on any conflict.
//
// Layout:
//
//	crypto/     scrypt KDF, AES-256-CTR + Poly1305-AES, key files
//	chunker/    Rabin CDC chunking (P1-T9)
//	backend/    repository storage (P1-T10)
//	pack/       pack files (P1-T11)
//	index/      index files + master index (P1-T12)
//	tree/       tree blobs (P1-T13)
//	snapshot/   snapshot documents (P1-T14)
//	repository/ repository lifecycle (P1-T15)
//	archiver/   file walking and backup (P1-T16)
//
// Secrets (passwords, master keys) never appear in errors or logs; use
// RedactedError for any error that wraps secret-adjacent input.
package restic
