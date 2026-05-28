package addomain

import (
	"context"
	"errors"
	"sync"
)

// DynamicDirectory is a Directory whose underlying LDAP config is
// looked up per call from a Provider function. Lets the operator
// change AD credentials through the portal without restarting the
// server: each PrepareComputer call reads the current config and
// dials a fresh connection.
//
// Dialing per call is the same model NewLDAPDirectory already uses, so
// this is just a thin indirection. The cost is one extra config-lookup
// (which the runtime package serves from cache).
type DynamicDirectory struct {
	Provider func(context.Context) LDAPConfig

	mu      sync.Mutex
	current Directory
	currCfg LDAPConfig
}

// Ensure DynamicDirectory satisfies Directory.
var _ Directory = (*DynamicDirectory)(nil)

func (d *DynamicDirectory) dir(ctx context.Context) (Directory, error) {
	cfg := d.Provider(ctx)
	if cfg.URL == "" {
		return nil, errors.New("AD not configured")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Reuse the inner directory if the config hasn't moved. LDAPConfig
	// is a value type so equality is field-wise.
	if d.current != nil && d.currCfg == cfg {
		return d.current, nil
	}
	d.current = NewLDAPDirectory(cfg)
	d.currCfg = cfg
	return d.current, nil
}

func (d *DynamicDirectory) FindComputer(ctx context.Context, name string) (string, error) {
	inner, err := d.dir(ctx)
	if err != nil {
		return "", err
	}
	return inner.FindComputer(ctx, name)
}

func (d *DynamicDirectory) DeleteComputer(ctx context.Context, dn string) error {
	inner, err := d.dir(ctx)
	if err != nil {
		return err
	}
	return inner.DeleteComputer(ctx, dn)
}

func (d *DynamicDirectory) CreateComputer(ctx context.Context, ou, name string) (string, error) {
	inner, err := d.dir(ctx)
	if err != nil {
		return "", err
	}
	return inner.CreateComputer(ctx, ou, name)
}

func (d *DynamicDirectory) SetGroupMemberships(ctx context.Context, computerDN string, groups []string) error {
	inner, err := d.dir(ctx)
	if err != nil {
		return err
	}
	return inner.SetGroupMemberships(ctx, computerDN, groups)
}

func (d *DynamicDirectory) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil {
		return d.current.Close()
	}
	return nil
}
