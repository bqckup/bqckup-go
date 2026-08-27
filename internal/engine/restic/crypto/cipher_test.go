package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testKeys(t *testing.T) (master *MasterKey, enc [32]byte, macK, macR [16]byte) {
	t.Helper()
	var err error
	master, err = NewRandomMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	return master, master.Encrypt, master.MACK, master.MACR
}

func TestSealOpenRoundTrip(t *testing.T) {
	_, enc, macK, macR := testKeys(t)
	big := make([]byte, 1<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	for name, plaintext := range map[string][]byte{
		"empty": {},
		"1byte": {0x42},
		"1mib":  big,
	} {
		t.Run(name, func(t *testing.T) {
			sealed, err := Seal(nil, plaintext, enc, macK, macR)
			if err != nil {
				t.Fatal(err)
			}
			if len(sealed) != len(plaintext)+Extension {
				t.Fatalf("sealed length = %d, want %d", len(sealed), len(plaintext)+Extension)
			}
			opened, err := Open(nil, sealed, enc, macK, macR)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(opened, plaintext) {
				t.Fatal("round trip mismatch")
			}
		})
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	_, enc, macK, macR := testKeys(t)
	plaintext := bytes.Repeat([]byte{0xab}, 100)
	sealed, err := Seal(nil, plaintext, enc, macK, macR)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[IVSize+10] ^= 0x01 // flip one ciphertext byte
	if _, err := Open(nil, tampered, enc, macK, macR); !errors.Is(err, errMACMismatch) {
		t.Fatalf("want errMACMismatch, got %v", err)
	}
}

func TestOpenRejectsShortCiphertext(t *testing.T) {
	_, enc, macK, macR := testKeys(t)
	if _, err := Open(nil, []byte("too short"), enc, macK, macR); err == nil {
		t.Fatal("want error for short ciphertext")
	}
}

func TestMasterKeyJSONRoundTrip(t *testing.T) {
	master, _, _, _ := testKeys(t)
	doc, err := master.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var restored MasterKey
	if err := restored.UnmarshalJSON(doc); err != nil {
		t.Fatal(err)
	}
	if restored != *master {
		t.Fatal("master key JSON round trip mismatch")
	}
}

func TestKeyFileRoundTrip(t *testing.T) {
	const password = "s3cret-password-for-testing"
	master, _, _, _ := testKeys(t)
	kf, err := NewKeyFile(password, "tester", "test-host", master, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if kf.KDF != "scrypt" || kf.N != ScryptN || kf.R != ScryptR || kf.P != ScryptP {
		t.Fatalf("unexpected KDF params: %+v", kf)
	}
	if len(kf.Salt) != scryptSaltLen {
		t.Fatalf("salt length = %d, want %d", len(kf.Salt), scryptSaltLen)
	}
	doc, err := json.Marshal(kf)
	if err != nil {
		t.Fatal(err)
	}
	var parsed KeyFile
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	restored, err := parsed.MasterKey(password)
	if err != nil {
		t.Fatal(err)
	}
	if *restored != *master {
		t.Fatal("decrypted master key does not match")
	}
}

func TestKeyFileWrongPassword(t *testing.T) {
	const password = "correct-password"
	master, _, _, _ := testKeys(t)
	kf, err := NewKeyFile(password, "tester", "test-host", master, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = kf.MasterKey("wrong-password")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("want ErrInvalidPassword, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") || strings.Contains(err.Error(), password) {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

func TestKeyFileTamperedData(t *testing.T) {
	const password = "correct-password"
	master, _, _, _ := testKeys(t)
	kf, err := NewKeyFile(password, "tester", "test-host", master, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	kf.Data[len(kf.Data)/2] ^= 0x01
	if _, err := kf.MasterKey(password); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("tampered key file: want ErrInvalidPassword, got %v", err)
	}
}

func TestKeyFileEmptyPasswordRejected(t *testing.T) {
	master, _, _, _ := testKeys(t)
	if _, err := NewKeyFile("", "tester", "test-host", master, time.Now()); err == nil {
		t.Fatal("want error for empty password")
	}
}

func TestDeriveDeterministic(t *testing.T) {
	kf := &KeyFile{KDF: "scrypt", N: 1024, R: 1, P: 1, Salt: bytes.Repeat([]byte{7}, 64)}
	first, err := kf.derive("same-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := kf.derive("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("derive is not deterministic")
	}
}

// TestRestic0164KeyFile decrypts a key file created by the REAL restic
// binary (0.16.4). The key-file format is identical in repository v1 and v2,
// so this proves our reader accepts upstream files, including upstream's
// self-calibrated scrypt parameters (N=32768, r=8, p=8 here).
// Fixture password: bqckup-fixture-password (created with restic init).
func TestRestic0164KeyFile(t *testing.T) {
	const fixturePassword = "bqckup-fixture-password"
	data, err := os.ReadFile("testdata/restic-0164.key.json")
	if err != nil {
		t.Fatal(err)
	}
	var kf KeyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		t.Fatal(err)
	}
	if kf.N != 32768 || kf.R != 8 || kf.P != 8 {
		t.Fatalf("fixture parameters changed? N=%d r=%d p=%d", kf.N, kf.R, kf.P)
	}
	master, err := kf.MasterKey(fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	if master.Encrypt == [32]byte{} || master.MACK == [16]byte{} || master.MACR == [16]byte{} {
		t.Fatal("decrypted master key has empty fields")
	}
	// The wrong password must fail on the same upstream file.
	if _, err := kf.MasterKey("wrong-password"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("want ErrInvalidPassword for wrong password, got %v", err)
	}
}
