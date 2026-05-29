package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownload_RejectsBadHash(t *testing.T) {
	body := []byte("the real bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "agent.new")
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err := Download(context.Background(), srv.URL, dst, wrongHash, false)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256 mismatch error, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("dst file should have been cleaned up on mismatch, stat=%v", statErr)
	}
}

func TestDownload_HappyPath(t *testing.T) {
	body := []byte("the new agent binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "agent.new")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := Download(context.Background(), srv.URL, dst, hash, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("downloaded body mismatch")
	}
}

func TestDownload_RejectsEmptyArgs(t *testing.T) {
	if err := Download(context.Background(), "", "/tmp/x", "abc", false); err == nil {
		t.Error("expected error for empty url")
	}
	if err := Download(context.Background(), "http://x", "/tmp/x", "", false); err == nil {
		t.Error("expected error for empty sha256")
	}
}

func TestDownload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "agent.new")
	err := Download(context.Background(), srv.URL, dst, "abc", false)
	if err == nil {
		t.Error("expected error on 404 response")
	}
}

func TestSiblingPath_HasSuffix(t *testing.T) {
	p, err := SiblingPath(".new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, ".new") {
		t.Errorf("SiblingPath should end with the suffix; got %q", p)
	}
}
