package addomain

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"

	ldap "github.com/go-ldap/ldap/v3"
)

const defaultPoolSize = 10

// LDAPConfig is the operator-supplied AD connection configuration.
type LDAPConfig struct {
	// URL is the LDAP server URL, e.g. "ldaps://dc.corp.example:636".
	URL string
	// BindDN is the service account, e.g. "CN=autodeploy,OU=Service
	// Accounts,DC=corp,DC=example".
	BindDN string
	// BindPassword is the service-account password. Treat as secret.
	BindPassword string
	// SearchBase is the AD subtree the service operates within, e.g.
	// "DC=corp,DC=example".
	SearchBase string
	// SkipTLSVerify disables certificate verification. Production should
	// never need this; useful for lab DCs with self-signed certs.
	SkipTLSVerify bool
}

// LDAPDirectory is the real AD backend backed by a connection pool.
// Idle connections are reused across operations to avoid the cost of
// TLS handshake + LDAP bind on every call.
type LDAPDirectory struct {
	cfg     LDAPConfig
	mu      sync.Mutex
	pool    []*ldap.Conn
	maxPool int
	closed  bool
}

// NewLDAPDirectory returns a Directory bound to cfg with connection pooling.
func NewLDAPDirectory(cfg LDAPConfig) *LDAPDirectory {
	return &LDAPDirectory{cfg: cfg, maxPool: defaultPoolSize}
}

// Close drains the connection pool and closes all idle connections.
func (l *LDAPDirectory) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for _, c := range l.pool {
		_ = c.Close()
	}
	l.pool = nil
	return nil
}

// getConn returns a pooled connection or dials a new one.
func (l *LDAPDirectory) getConn(ctx context.Context) (*ldap.Conn, error) {
	l.mu.Lock()
	for len(l.pool) > 0 {
		n := len(l.pool) - 1
		c := l.pool[n]
		l.pool[n] = nil // avoid leak in underlying array
		l.pool = l.pool[:n]
		l.mu.Unlock()

		if c.IsClosing() {
			_ = c.Close()
			l.mu.Lock()
			continue
		}
		return c, nil
	}
	l.mu.Unlock()
	return l.dial(ctx)
}

// putConn returns a connection to the pool, or closes it if the pool is
// full or the directory has been closed.
func (l *LDAPDirectory) putConn(c *ldap.Conn) {
	if c == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.pool) >= l.maxPool {
		_ = c.Close()
		return
	}
	l.pool = append(l.pool, c)
}

// discardConn closes a connection that may be in a bad state instead of
// returning it to the pool.
func (l *LDAPDirectory) discardConn(c *ldap.Conn) {
	if c != nil {
		_ = c.Close()
	}
}

func (l *LDAPDirectory) dial(ctx context.Context) (*ldap.Conn, error) {
	if l.cfg.URL == "" {
		return nil, ErrNotConnected
	}
	c, err := ldap.DialURL(l.cfg.URL, ldap.DialWithTLSConfig(&tls.Config{
		InsecureSkipVerify: l.cfg.SkipTLSVerify,
	}))
	if err != nil {
		return nil, fmt.Errorf("ldap dial %s: %w", l.cfg.URL, err)
	}
	if err := c.Bind(l.cfg.BindDN, l.cfg.BindPassword); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ldap bind: %w", err)
	}
	return c, nil
}

func (l *LDAPDirectory) FindComputer(ctx context.Context, name string) (string, error) {
	c, err := l.getConn(ctx)
	if err != nil {
		return "", err
	}
	filter := fmt.Sprintf("(&(objectClass=computer)(cn=%s))", ldap.EscapeFilter(name))
	req := ldap.NewSearchRequest(l.cfg.SearchBase, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 1, 30, false,
		filter, []string{"distinguishedName"}, nil)
	res, err := c.Search(req)
	if err != nil {
		l.discardConn(c)
		return "", err
	}
	l.putConn(c)
	if len(res.Entries) == 0 {
		return "", nil
	}
	return res.Entries[0].DN, nil
}

func (l *LDAPDirectory) DeleteComputer(ctx context.Context, dn string) error {
	c, err := l.getConn(ctx)
	if err != nil {
		return err
	}
	if err := c.Del(ldap.NewDelRequest(dn, nil)); err != nil {
		l.discardConn(c)
		return err
	}
	l.putConn(c)
	return nil
}

func (l *LDAPDirectory) CreateComputer(ctx context.Context, ou, name string) (string, error) {
	c, err := l.getConn(ctx)
	if err != nil {
		return "", err
	}
	dn := computerDN(name, ou)
	req := ldap.NewAddRequest(dn, nil)
	req.Attribute("objectClass", []string{"top", "computer"})
	req.Attribute("cn", []string{name})
	req.Attribute("sAMAccountName", []string{name + "$"})
	// userAccountControl 4096 = WORKSTATION_TRUST_ACCOUNT.
	req.Attribute("userAccountControl", []string{"4096"})
	if err := c.Add(req); err != nil {
		l.discardConn(c)
		return "", fmt.Errorf("create %s: %w", dn, err)
	}
	l.putConn(c)
	return dn, nil
}

func (l *LDAPDirectory) SetGroupMemberships(ctx context.Context, computerDN string, groups []string) error {
	c, err := l.getConn(ctx)
	if err != nil {
		return err
	}
	// Find current groups: groups whose 'member' includes computerDN.
	filter := fmt.Sprintf("(&(objectClass=group)(member=%s))", ldap.EscapeFilter(computerDN))
	req := ldap.NewSearchRequest(l.cfg.SearchBase, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 0, 60, false,
		filter, []string{"distinguishedName", "cn"}, nil)
	res, err := c.Search(req)
	if err != nil {
		l.discardConn(c)
		return err
	}
	want := map[string]bool{}
	for _, g := range groups {
		want[strings.ToLower(g)] = true
	}
	// Remove from groups we no longer want; tick off groups we already are
	// in but still want.
	for _, e := range res.Entries {
		groupName := e.GetAttributeValue("cn")
		key := strings.ToLower(groupName)
		if want[key] {
			delete(want, key) // already a member
			continue
		}
		mod := ldap.NewModifyRequest(e.DN, nil)
		mod.Delete("member", []string{computerDN})
		if err := c.Modify(mod); err != nil {
			l.discardConn(c)
			return fmt.Errorf("remove %s from %s: %w", computerDN, e.DN, err)
		}
	}
	// Add to remaining wanted groups.
	for groupName := range want {
		groupDN, err := l.findGroupDN(c, groupName)
		if err != nil {
			l.discardConn(c)
			return err
		}
		if groupDN == "" {
			l.discardConn(c)
			return fmt.Errorf("group %s not found", groupName)
		}
		mod := ldap.NewModifyRequest(groupDN, nil)
		mod.Add("member", []string{computerDN})
		if err := c.Modify(mod); err != nil {
			l.discardConn(c)
			return fmt.Errorf("add %s to %s: %w", computerDN, groupDN, err)
		}
	}
	l.putConn(c)
	return nil
}

// RenameComputer issues an LDAP MODRDN with the same parent (so the
// object stays in its current OU; only the CN changes).
func (l *LDAPDirectory) RenameComputer(ctx context.Context, dn, newName string) (string, error) {
	c, err := l.getConn(ctx)
	if err != nil {
		return "", err
	}
	req := ldap.NewModifyDNRequest(dn, "CN="+ldapEscape(newName), true, "")
	if err := c.ModifyDN(req); err != nil {
		l.discardConn(c)
		return "", fmt.Errorf("rename %s -> %s: %w", dn, newName, err)
	}
	// Compose the new DN: replace the leading RDN.
	parent := dn
	if i := strings.Index(dn, ","); i >= 0 {
		parent = dn[i+1:]
	}
	newDN := "CN=" + ldapEscape(newName) + "," + parent
	// Also update sAMAccountName to match the new CN (workstation
	// accounts have sAMAccountName = "<cn>$"). Some AD callers fail
	// secure-channel checks if the two drift.
	mod := ldap.NewModifyRequest(newDN, nil)
	mod.Replace("sAMAccountName", []string{newName + "$"})
	if err := c.Modify(mod); err != nil {
		// Non-fatal: the rename itself completed. Discard the conn
		// since the error may indicate a bad state.
		l.discardConn(c)
		return newDN, fmt.Errorf("rename ok; sAMAccountName update failed: %w", err)
	}
	l.putConn(c)
	return newDN, nil
}

func (l *LDAPDirectory) findGroupDN(c *ldap.Conn, name string) (string, error) {
	filter := fmt.Sprintf("(&(objectClass=group)(cn=%s))", ldap.EscapeFilter(name))
	req := ldap.NewSearchRequest(l.cfg.SearchBase, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 1, 30, false,
		filter, []string{"distinguishedName"}, nil)
	res, err := c.Search(req)
	if err != nil {
		return "", err
	}
	if len(res.Entries) == 0 {
		return "", nil
	}
	return res.Entries[0].DN, nil
}
