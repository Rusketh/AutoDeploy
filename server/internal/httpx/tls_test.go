package httpx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	if err := generateSelfSigned(cert, key, "127.0.0.1:8443"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{cert, key} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", f)
		}
		// File must be owner-readable but not world-readable.
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has loose perms %o", f, info.Mode().Perm())
		}
	}
}
