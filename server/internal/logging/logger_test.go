package logging

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretStringRedacts(t *testing.T) {
	s := NewSecret("hunter2")
	if got := s.String(); got != "[REDACTED]" {
		t.Errorf("Secret.String = %q, want [REDACTED]", got)
	}
	if strings.Contains(s.String(), "hunter2") {
		t.Error("Secret value leaked through String()")
	}
}

func TestSecretJSONRedacts(t *testing.T) {
	s := NewSecret("hunter2")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("Secret value leaked through JSON: %s", b)
	}
	if string(b) != `"[REDACTED]"` {
		t.Errorf("Secret JSON = %s, want \"[REDACTED]\"", b)
	}
}

func TestSecretReveal(t *testing.T) {
	s := NewSecret("hunter2")
	if got := s.Reveal(); got != "hunter2" {
		t.Errorf("Reveal = %q, want hunter2", got)
	}
}

// Container struct including a Secret must not leak the secret in JSON.
func TestSecretInStructRedacts(t *testing.T) {
	type record struct {
		Name string `json:"name"`
		PIN  Secret `json:"pin"`
	}
	r := record{Name: "lab-01", PIN: NewSecret("123456")}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "123456") {
		t.Errorf("PIN leaked: %s", b)
	}
}
