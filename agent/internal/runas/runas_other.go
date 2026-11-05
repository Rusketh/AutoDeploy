//go:build !windows

package runas

import (
	"context"
	"io"
)

// Session is unavailable off Windows; Logon and LoggedOnUser always fail.
type Session struct{ Account string }

// Logon is unsupported off Windows.
func Logon(account, password string) (*Session, error) { return nil, ErrUnsupported }

// LoggedOnUser is unsupported off Windows.
func LoggedOnUser() (*Session, error) { return nil, ErrUnsupported }

// Close is a no-op.
func (s *Session) Close() {}

// Expand returns s unchanged.
func (s *Session) Expand(str string) string { return str }

// GrantDir is unsupported off Windows.
func (s *Session) GrantDir(dir string) error { return ErrUnsupported }

// Launch is unsupported off Windows.
func (s *Session) Launch(ctx context.Context, name string, args []string, dir, stdin string, stdout, stderr io.Writer) (int, error) {
	return -1, ErrUnsupported
}
