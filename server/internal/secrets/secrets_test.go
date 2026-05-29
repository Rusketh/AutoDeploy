package secrets

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := Open("", filepath.Join(dir, "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Encrypt("hunter2-not-real")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "hunter2") {
		t.Error("plaintext leaked through ciphertext")
	}
	pt, err := b.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if pt != "hunter2-not-real" {
		t.Errorf("round trip failed: %q", pt)
	}
}

func TestTamperingRejected(t *testing.T) {
	dir := t.TempDir()
	b, _ := Open("", filepath.Join(dir, "k.bin"))
	ct, _ := b.Encrypt("x")
	// Corrupt a byte in the binary ciphertext, not the base64
	// envelope. The previous form -- mutating ct[len-2] from 'A' to
	// 'B' or vice versa -- was a ~6% flake: in a StdEncoding string
	// that ends in one '=' of padding, the second-to-last char's low
	// 2 bits are padding bits. When the original char happened to
	// fall in 'A'..'D' (high 4 bits zero) the test only flipped a
	// padding bit, the decoded byte stream was identical, AEAD
	// authentication passed, and the test failed with err=nil.
	// Round-tripping through binary makes the corruption
	// unambiguous: we flip a bit on the AEAD tag.
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("decode original ciphertext: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	mangled := base64.StdEncoding.EncodeToString(raw)
	if _, err := b.Decrypt(mangled); err != ErrTampered {
		t.Errorf("expected ErrTampered, got %v", err)
	}
}

func TestKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.bin")
	if _, err := Open("", path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("key file has loose perms %o", info.Mode().Perm())
	}
}

func TestWrongKeyCannotDecrypt(t *testing.T) {
	dir := t.TempDir()
	b1, _ := Open("", filepath.Join(dir, "k1.bin"))
	b2, _ := Open("", filepath.Join(dir, "k2.bin"))
	ct, _ := b1.Encrypt("x")
	if _, err := b2.Decrypt(ct); err != ErrTampered {
		t.Errorf("expected ErrTampered with wrong key, got %v", err)
	}
}
