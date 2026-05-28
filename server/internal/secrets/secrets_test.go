package secrets

import (
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
	// Flip a byte (in the base64 alphabet).
	mangled := []byte(ct)
	if mangled[len(mangled)-2] == 'A' {
		mangled[len(mangled)-2] = 'B'
	} else {
		mangled[len(mangled)-2] = 'A'
	}
	if _, err := b.Decrypt(string(mangled)); err != ErrTampered {
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
