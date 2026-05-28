// Package secrets owns the at-rest encryption used for BitLocker PINs and
// recovery keys (and any other secret column added later). AES-256-GCM
// with a key sourced from the AUTODEPLOY_SECRETS_KEY env var, or
// auto-generated to data/secrets-key.bin (0600) on first start.
//
// The key NEVER appears in any log line. The encrypted form is what the
// rest of the system handles; only the audited retrieval boundary calls
// Decrypt.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Box wraps an AES-256-GCM AEAD cipher. Construct via Open.
type Box struct {
	aead cipher.AEAD
}

// Open returns a Box. envKey, if non-empty, is the hex-encoded 32-byte
// key (preferred for production). Otherwise the key is loaded from
// keyPath, generated there if absent (0600 perms).
func Open(envKey, keyPath string) (*Box, error) {
	key, err := resolveKey(envKey, keyPath)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func resolveKey(envKey, keyPath string) ([]byte, error) {
	if envKey != "" {
		b, err := hex.DecodeString(envKey)
		if err != nil {
			return nil, fmt.Errorf("AUTODEPLOY_SECRETS_KEY: %w", err)
		}
		return b, nil
	}
	if keyPath == "" {
		return nil, errors.New("no secrets key configured")
	}
	if b, err := os.ReadFile(keyPath); err == nil {
		return b, nil
	}
	// Generate on first start.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, b[:], 0o600); err != nil {
		return nil, err
	}
	return b[:], nil
}

// Encrypt seals plaintext and returns base64-encoded ciphertext.
func (b *Box) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Returns ErrTampered if the ciphertext fails
// authentication.
func (b *Box) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns+b.aead.Overhead() {
		return "", ErrTampered
	}
	nonce := raw[:ns]
	body := raw[ns:]
	out, err := b.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", ErrTampered
	}
	return string(out), nil
}

// ErrTampered indicates a ciphertext failed authentication during
// decryption.
var ErrTampered = errors.New("ciphertext authentication failed (tampered or wrong key)")
