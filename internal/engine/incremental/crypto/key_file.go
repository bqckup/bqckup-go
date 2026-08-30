package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/scrypt"
)

// KeyFile is the JSON document stored under keys/<sha256>. Only the Data
// field is sealed; the rest (including the scrypt parameters) is plaintext
// so any reader can derive keys before authenticating anything.
// Verified field names: created, username, hostname, kdf, N, r, p, salt, data.
type KeyFile struct {
	Created  time.Time `json:"created"`
	Username string    `json:"username"`
	Hostname string    `json:"hostname"`
	KDF      string    `json:"kdf"`
	N        int       `json:"N"`
	R        int       `json:"r"`
	P        int       `json:"p"`
	Salt     []byte    `json:"salt"`
	Data     []byte    `json:"data"` // IV || AES-CTR(master key JSON) || MAC
}

// Fixed scrypt parameters written by this engine. Restic accepts any valid
// parameters stored in a key file; it calibrates its own on creation.
const (
	ScryptN       = 65536
	ScryptR       = 8
	ScryptP       = 1
	kdfOutputLen  = 64
	scryptSaltLen = 64
)

// ErrInvalidPassword reports a password that fails to open the key file.
var ErrInvalidPassword = errors.New("invalid repository password")

// NewKeyFile seals master with password and returns the key-file document.
func NewKeyFile(password, username, hostname string, master *MasterKey, created time.Time) (*KeyFile, error) {
	if password == "" {
		return nil, errors.New("repository password must not be empty")
	}
	salt := make([]byte, scryptSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate key salt: %w", err)
	}
	kf := &KeyFile{
		Created:  created.UTC(),
		Username: username,
		Hostname: hostname,
		KDF:      "scrypt",
		N:        ScryptN,
		R:        ScryptR,
		P:        ScryptP,
		Salt:     salt,
	}
	keys, err := kf.derive(password)
	if err != nil {
		return nil, err
	}
	plain, err := master.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal master key: %w", err)
	}
	kf.Data, err = Seal(nil, plain, keys.encrypt, keys.macK, keys.macR)
	if err != nil {
		return nil, err
	}
	return kf, nil
}

// MasterKey derives keys from password using the parameters stored in the
// key file and decrypts the sealed master key.
func (kf *KeyFile) MasterKey(password string) (*MasterKey, error) {
	keys, err := kf.derive(password)
	if err != nil {
		return nil, err
	}
	plain, err := Open(nil, kf.Data, keys.encrypt, keys.macK, keys.macR)
	if errors.Is(err, errMACMismatch) {
		// No secret in the message: the wrong password only manifests as a
		// failed MAC on the key file.
		return nil, fmt.Errorf("%w: the repository key file could not be opened", ErrInvalidPassword)
	}
	if err != nil {
		return nil, err
	}
	var master MasterKey
	if err := master.UnmarshalJSON(plain); err != nil {
		return nil, fmt.Errorf("parse master key: %w", err)
	}
	return &master, nil
}

type derivedKeys struct {
	encrypt [32]byte
	macK    [16]byte
	macR    [16]byte
}

// derive runs scrypt and splits the 64-byte output into
// 32B AES key + 16B Poly1305 AES key + 16B Poly1305 r.
func (kf *KeyFile) derive(password string) (derivedKeys, error) {
	var keys derivedKeys
	if kf.KDF != "scrypt" {
		return keys, fmt.Errorf("unsupported KDF %q", kf.KDF)
	}
	if kf.N < 2 || kf.R < 1 || kf.P < 1 {
		return keys, fmt.Errorf("invalid scrypt parameters N=%d r=%d p=%d", kf.N, kf.R, kf.P)
	}
	output, err := scrypt.Key([]byte(password), kf.Salt, kf.N, kf.R, kf.P, kdfOutputLen)
	if err != nil {
		return keys, fmt.Errorf("derive key from password: %w", err)
	}
	copy(keys.encrypt[:], output[:32])
	copy(keys.macK[:], output[32:48])
	copy(keys.macR[:], output[48:64])
	return keys, nil
}
