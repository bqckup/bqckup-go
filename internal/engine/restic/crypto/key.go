package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/poly1305"
)

// poly1305Sum is indirection so tests never import x/crypto directly.
func poly1305Sum(msg []byte, key *[32]byte) [16]byte {
	var out [16]byte
	poly1305.Sum(&out, msg, key)
	return out
}

// MasterKey encrypts and authenticates all repository data.
// Stored inside the encrypted key file as
// {"mac":{"k":...,"r":...},"encrypt":...} (base64 values).
type MasterKey struct {
	Encrypt [32]byte // AES-256 key
	MACK    [16]byte // AES-128 key for the Poly1305 one-time key
	MACR    [16]byte // Poly1305 key r (random, unmasked)
}

// NewRandomMasterKey generates a fresh master key.
func NewRandomMasterKey() (*MasterKey, error) {
	key := &MasterKey{}
	if _, err := rand.Read(key.Encrypt[:]); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	if _, err := rand.Read(key.MACK[:]); err != nil {
		return nil, fmt.Errorf("generate mac key: %w", err)
	}
	if _, err := rand.Read(key.MACR[:]); err != nil {
		return nil, fmt.Errorf("generate mac r: %w", err)
	}
	return key, nil
}

// Seal encrypts plaintext with this key.
func (k *MasterKey) Seal(dst, plaintext []byte) ([]byte, error) {
	return Seal(dst, plaintext, k.Encrypt, k.MACK, k.MACR)
}

// Open verifies and decrypts ciphertext with this key.
func (k *MasterKey) Open(dst, ciphertext []byte) ([]byte, error) {
	return Open(dst, ciphertext, k.Encrypt, k.MACK, k.MACR)
}

func (k MasterKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(masterKeyJSON{
		MAC: macKeyJSON{
			K: base64.StdEncoding.EncodeToString(k.MACK[:]),
			R: base64.StdEncoding.EncodeToString(k.MACR[:]),
		},
		Encrypt: base64.StdEncoding.EncodeToString(k.Encrypt[:]),
	})
}

func (k *MasterKey) UnmarshalJSON(data []byte) error {
	var doc masterKeyJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	macK, err := base64.StdEncoding.DecodeString(doc.MAC.K)
	if err != nil || len(macK) != 16 {
		return fmt.Errorf("invalid mac key k")
	}
	macR, err := base64.StdEncoding.DecodeString(doc.MAC.R)
	if err != nil || len(macR) != 16 {
		return fmt.Errorf("invalid mac key r")
	}
	encrypt, err := base64.StdEncoding.DecodeString(doc.Encrypt)
	if err != nil || len(encrypt) != 32 {
		return fmt.Errorf("invalid encryption key")
	}
	copy(k.MACK[:], macK)
	copy(k.MACR[:], macR)
	copy(k.Encrypt[:], encrypt)
	return nil
}

type masterKeyJSON struct {
	MAC     macKeyJSON `json:"mac"`
	Encrypt string     `json:"encrypt"`
}

type macKeyJSON struct {
	K string `json:"k"`
	R string `json:"r"`
}
