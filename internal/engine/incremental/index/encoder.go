package index

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
	"github.com/klauspost/compress/zstd"
)

// versionByte prefixes the plaintext of every index file.
const versionByte = 0x02

// Seal encodes, compresses, and encrypts an index; the returned bytes are
// the complete index file content. The file name is incremental.Hash(sealed).
func Seal(idx Index, master *crypto.MasterKey) ([]byte, error) {
	plaintext, err := encode(idx)
	if err != nil {
		return nil, err
	}
	sealed, err := master.Seal(nil, plaintext)
	if err != nil {
		return nil, err
	}
	return sealed, nil
}

// Open decrypts and decodes an index file's content.
func Open(data []byte, master *crypto.MasterKey) (Index, error) {
	plaintext, err := master.Open(nil, data)
	if err != nil {
		return Index{}, fmt.Errorf("index: decrypt: %w", err)
	}
	return decode(plaintext)
}

// encode produces 0x02 || zstd(JSON).
func encode(idx Index) ([]byte, error) {
	doc, err := json.Marshal(idx)
	if err != nil {
		return nil, fmt.Errorf("index: marshal json: %w", err)
	}
	plaintext := make([]byte, 1, 1+len(doc)/2)
	plaintext[0] = versionByte
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("index: create zstd encoder: %w", err)
	}
	defer encoder.Close()
	compressed := encoder.EncodeAll(doc, plaintext[:1])
	return compressed, nil
}

// decode reverses encode.
func decode(plaintext []byte) (Index, error) {
	if len(plaintext) < 1 {
		return Index{}, errors.New("index: empty plaintext")
	}
	if plaintext[0] != versionByte {
		return Index{}, fmt.Errorf("index: unsupported version byte 0x%02x, want 0x%02x", plaintext[0], versionByte)
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return Index{}, fmt.Errorf("index: create zstd decoder: %w", err)
	}
	defer decoder.Close()
	doc, err := decoder.DecodeAll(plaintext[1:], nil)
	if err != nil {
		return Index{}, fmt.Errorf("index: zstd decode: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(doc, &idx); err != nil {
		return Index{}, fmt.Errorf("index: unmarshal json: %w", err)
	}
	return idx, nil
}
