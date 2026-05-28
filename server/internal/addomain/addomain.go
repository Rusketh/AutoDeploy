// Package addomain is the Domain Integration Service. It performs the
// server-side Active Directory operations described in the design:
// computer-object delete-and-replace at the bound name + OU, and group
// membership reconciliation from the binding.
//
// The Directory interface decouples the AD operations from the LDAP wire
// protocol so tests can use FakeDirectory and production uses
// LDAPDirectory (backed by go-ldap/ldap/v3). The Service ties the two
// together and is what the rest of the server calls.
//
// Security note: the service account credentials are secrets. They are
// loaded from environment variables, never logged, and only passed
// directly to the LDAP bind step.
package addomain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/logging"
)

// Directory is the abstraction the Service speaks to. Implementations:
//   LDAPDirectory  - real AD via go-ldap.
//   FakeDirectory  - in-memory, used by tests.
type Directory interface {
	// FindComputer returns the DN of the computer object whose CN matches
	// name, anywhere in the configured search base. Returns ("", nil) when
	// no object is found.
	FindComputer(ctx context.Context, name string) (string, error)
	// DeleteComputer removes the object at dn.
	DeleteComputer(ctx context.Context, dn string) error
	// CreateComputer creates a computer object named name in the given OU.
	// Returns the new DN.
	CreateComputer(ctx context.Context, ou, name string) (string, error)
	// SetGroupMemberships removes the computer from every group it is in
	// and adds it to exactly the groups listed. Idempotent.
	SetGroupMemberships(ctx context.Context, computerDN string, groups []string) error
	// Close releases any underlying connection. No-op for FakeDirectory.
	Close() error
}

// Service performs the high-level operations Phase 10 requires.
type Service struct {
	Dir    Directory
	Log    *slog.Logger
	// Enabled gates the whole thing. When false (no AD integration
	// configured), PrepareComputer is a successful no-op so the rest of
	// the deployment flow keeps working without AD.
	Enabled bool
}

// PrepareComputer is the delete-and-replace lifecycle. If a computer
// object already exists at name, it is deleted, then a fresh one is
// created at ou and group memberships are reconciled from the binding.
//
// Returns the resulting computer DN, or "" if the service is disabled.
func (s *Service) PrepareComputer(ctx context.Context, name, ou string, groups []string) (string, error) {
	if s == nil || !s.Enabled {
		return "", nil
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("computer name required")
	}
	if strings.TrimSpace(ou) == "" {
		return "", fmt.Errorf("target OU required")
	}
	s.log().Info("addomain.prepare.start",
		slog.String("actor", "server"),
		slog.String("target", name),
		slog.String("ou", ou),
	)
	existingDN, err := s.Dir.FindComputer(ctx, name)
	if err != nil {
		return "", fmt.Errorf("find existing %s: %w", name, err)
	}
	if existingDN != "" {
		s.log().Info("addomain.prepare.delete_existing",
			slog.String("actor", "server"),
			slog.String("target", existingDN),
			slog.String("note", "delete-and-replace: SID/GUID will change; LAPS state on this object is lost"),
		)
		if err := s.Dir.DeleteComputer(ctx, existingDN); err != nil {
			return "", fmt.Errorf("delete %s: %w", existingDN, err)
		}
	}
	newDN, err := s.Dir.CreateComputer(ctx, ou, name)
	if err != nil {
		return "", fmt.Errorf("create %s in %s: %w", name, ou, err)
	}
	if err := s.Dir.SetGroupMemberships(ctx, newDN, groups); err != nil {
		// Partial failure: the object exists but groups are wrong. Log
		// loudly but do not unwind the object — the join will still
		// work; the operator can fix groups via the portal.
		s.log().Error("addomain.groups.partial",
			slog.String("target", newDN),
			slog.String("error", err.Error()))
	}
	s.log().Info("addomain.prepare.ok",
		slog.String("actor", "server"),
		slog.String("target", newDN),
		slog.Int("groups", len(groups)),
	)
	return newDN, nil
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ErrNotConnected indicates the LDAPDirectory has no active bind. Use to
// distinguish configuration errors from real AD errors in the caller.
var ErrNotConnected = errors.New("not connected to LDAP")

// nameToCN produces the CN portion of a DN from a plain machine name.
func nameToCN(name string) string { return "CN=" + ldapEscape(name) }

func ldapEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ',', '\\', '#', '+', '<', '>', ';', '"', '=':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// computerDN composes CN=<name>,<ou>.
func computerDN(name, ou string) string {
	return nameToCN(name) + "," + ou
}

// SecretAccess records that a code path read a secret value. The actual
// value never appears in logs.
func SecretAccess(log *slog.Logger, actor, what string) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("secret.access",
		slog.String("actor", actor),
		slog.String("target", what),
		slog.String("note", "value not logged"),
	)
}

// Compile-time check that logging.Secret can be used here without import
// cycle issues. The import is kept so the package is self-contained.
var _ = logging.Secret{}
