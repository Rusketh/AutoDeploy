// Package swaccess decides which software packages a machine's agent may
// read: a package's definition and its payload files are served only to an
// enrolled agent whose machine needs that package -- because it is in the
// software set of the machine's bound image, or because a "push software"
// job for it (or a package depending on it) is running on the machine.
// Dependencies are included in both cases, exactly as the agent installs them.
//
// Payload downloads ask once per file (a large package has thousands), so
// each machine's allowed set is cached briefly.
package swaccess

import (
	"context"
	"github.com/rusketh/autodeploy/server/internal/agentauth"
	"net/http"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
)

// AgentIDHeader carries the calling agent's server-minted id.
const AgentIDHeader = agentauth.HeaderAgentID

// cacheTTL bounds how stale a machine's allowed set may be. Short: a push
// that just started (or an image re-bind) is picked up within this window,
// and the agent retries a failed download on its next poll anyway.
const cacheTTL = 30 * time.Second

// Checker answers "may this agent read this package?".
type Checker struct {
	// Auth authenticates the downloading agent (its secret when the machine
	// has one; legacy machines by agent id or UUID header). nil falls back
	// to a legacy-accepting authenticator over Inventory.
	Auth      *agentauth.Authenticator
	Inventory *model.InventoryRepo
	Software  *model.SoftwarePackageRepo
	Bulk      *model.BulkRepo
	Resolver  *resolve.Resolver

	mu    sync.Mutex
	cache map[model.ID]cached
	now   func() time.Time
}

type cached struct {
	allowed map[model.ID]bool
	at      time.Time
}

// Machine authenticates the downloading agent. ok is false when it can't be
// identified or fails authentication.
func (c *Checker) Machine(req *http.Request) (model.MachineRecord, bool) {
	if c == nil {
		return model.MachineRecord{}, false
	}
	auth := c.Auth
	if auth == nil {
		auth = &agentauth.Authenticator{Inventory: c.Inventory, AllowLegacy: true}
	}
	caller, err := auth.Any(req, "")
	if err != nil {
		return model.MachineRecord{}, false
	}
	return caller.Machine, true
}

// Allowed reports whether machineID may read package pkgID.
func (c *Checker) Allowed(ctx context.Context, machineID, pkgID model.ID) bool {
	if c == nil {
		return false
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.mu.Lock()
	if e, ok := c.cache[machineID]; ok && now().Sub(e.at) < cacheTTL && e.allowed[pkgID] {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	// Not cached (or not in the cached set): recompute, so a push that
	// started a moment ago isn't refused for the rest of the TTL.
	allowed := c.compute(ctx, machineID)
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[model.ID]cached{}
	}
	c.cache[machineID] = cached{allowed: allowed, at: now()}
	c.mu.Unlock()
	return allowed[pkgID]
}

func (c *Checker) compute(ctx context.Context, machineID model.ID) map[model.ID]bool {
	var seeds []model.ID
	if c.Inventory != nil && c.Resolver != nil {
		if b, err := c.Inventory.GetBinding(ctx, machineID); err == nil && b.ImageID != nil {
			if res, err := c.Resolver.Resolve(ctx, *b.ImageID); err == nil {
				for _, link := range res.Software {
					seeds = append(seeds, link.PackageID)
				}
			}
		}
	}
	if c.Bulk != nil {
		if pushed, err := c.Bulk.RunningPushPackages(ctx, machineID); err == nil {
			seeds = append(seeds, pushed...)
		}
	}
	out := map[model.ID]bool{}
	if len(seeds) == 0 || c.Software == nil {
		return out
	}
	// Resolve each seed on its own: supersedence between an image package
	// and a pushed one must not hide either from the machine that asked
	// for it. Dependencies come along.
	for _, s := range seeds {
		ordered, _ := c.Software.ResolveOrder(ctx, []model.ID{s})
		for _, id := range ordered {
			out[id] = true
		}
		out[s] = true
	}
	return out
}
