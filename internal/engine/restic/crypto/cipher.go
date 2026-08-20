// Package crypto implements the restic v2 crypto envelope:
// AES-256-CTR encryption with a Poly1305-AES MAC, scrypt key derivation,
// and the encrypted key-file format. Every detail here was verified against
// the official restic source (see restic-format-verification.md §2.3-2.4).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
)

const (
	// Extension is the fixed envelope overhead: 16-byte IV + 16-byte MAC.
	Extension = 32
	IVSize    = 16
	MACSize   = 16
)

// errMACMismatch is returned by Open when the Poly1305-AES tag does not
// verify. Callers categorize it: on a key file it means the wrong password
// (ErrInvalidPassword); on repository data it means corruption.
var errMACMismatch = errors.New("message authentication failed")

// Seal encrypts plaintext into IV (16) || ciphertext || MAC (16).
// dst is reused when it has enough capacity.
func Seal(dst, plaintext []byte, encKey [32]byte, macK, macR [16]byte) ([]byte, error) {
	total := len(plaintext) + Extension
	if cap(dst) < total {
		dst = make([]byte, total)
	} else {
		dst = dst[:total]
	}
	iv := dst[:IVSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("generate iv: %w", err)
	}
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	cipher.NewCTR(block, iv).XORKeyStream(dst[IVSize:IVSize+len(plaintext)], plaintext)
	tag := mac(dst[IVSize:IVSize+len(plaintext)], iv, macK, macR)
	copy(dst[IVSize+len(plaintext):], tag[:])
	return dst, nil
}

// Open verifies the MAC and decrypts ciphertext (IV || ct || MAC) into dst.
func Open(dst, ciphertext []byte, encKey [32]byte, macK, macR [16]byte) ([]byte, error) {
	if len(ciphertext) < Extension {
		return nil, fmt.Errorf("ciphertext is too short: %d bytes", len(ciphertext))
	}
	ct := ciphertext[IVSize : len(ciphertext)-MACSize]
	expected := mac(ct, ciphertext[:IVSize], macK, macR)
	actual := ciphertext[len(ciphertext)-MACSize:]
	if subtle.ConstantTimeCompare(expected[:], actual) != 1 {
		return nil, errMACMismatch
	}
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	if cap(dst) < len(ct) {
		dst = make([]byte, len(ct))
	} else {
		dst = dst[:len(ct)]
	}
	cipher.NewCTR(block, ciphertext[:IVSize]).XORKeyStream(dst, ct)
	return dst, nil
}

// mac computes Poly1305_r(ciphertext) + AES_k(iv) mod 2^128.
// r is used as stored (random, unmasked); the RFC 7539 clamp is applied
// inside golang.org/x/crypto/poly1305, exactly like restic does.
func mac(ciphertext, iv []byte, k, r [16]byte) [16]byte {
	var key [32]byte
	copy(key[:16], r[:])
	block, err := aes.NewCipher(k[:])
	if err != nil {
		// aes.NewCipher cannot fail for a 16-byte key.
		panic(fmt.Sprintf("create mac cipher: %v", err))
	}
	// key[16:32] is zero; one CTR block yields the AES_k(iv) keystream.
	cipher.NewCTR(block, iv).XORKeyStream(key[16:], key[16:])
	var tag [16]byte
	sum := poly1305Sum(ciphertext, &key)
	copy(tag[:], sum[:])
	return tag
}
