package httpx

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// TrustedProxyMiddleware returns a handler that rewrites r.RemoteAddr
// using X-Forwarded-For when the immediate client IP falls within one
// of the trusted CIDRs. It also sets scheme from X-Forwarded-Proto.
// When cidrs is empty the handler is a no-op pass-through.
func TrustedProxyMiddleware(cidrs string, next http.Handler) http.Handler {
	nets := parseCIDRs(cidrs)
	if len(nets) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isTrusted(r.RemoteAddr, nets) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				clientIP := strings.TrimSpace(parts[0])
				if clientIP != "" {
					_, port, _ := net.SplitHostPort(r.RemoteAddr)
					if port == "" {
						port = "0"
					}
					r.RemoteAddr = net.JoinHostPort(clientIP, port)
				}
			}
			if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
				r.URL.Scheme = strings.ToLower(proto)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ProxyConfig holds the dynamic functions that control reverse proxy
// behaviour. Both are re-read on every request so operators can change
// them at runtime without a restart.
type ProxyConfig struct {
	CIDRsFn        func() string // trusted proxy CIDRs
	ExternalAccess func() string // "agent" restricts proxied traffic to agent endpoints
}

// DynamicTrustedProxyMiddleware rewrites RemoteAddr from X-Forwarded-For
// when the direct client IP is in the trusted CIDRs. When ExternalAccess
// returns "agent", requests arriving through a trusted proxy are
// restricted to agent-only paths (/api/v1/agent/*, /payload/*, /healthz).
func DynamicTrustedProxyMiddleware(pc ProxyConfig, next http.Handler) http.Handler {
	var (
		mu       sync.RWMutex
		lastCIDR string
		cached   []*net.IPNet
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := pc.CIDRsFn()
		mu.RLock()
		nets := cached
		same := current == lastCIDR
		mu.RUnlock()
		if !same {
			nets = parseCIDRs(current)
			mu.Lock()
			cached = nets
			lastCIDR = current
			mu.Unlock()
		}
		proxied := len(nets) > 0 && isTrusted(r.RemoteAddr, nets)
		if proxied {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				clientIP := strings.TrimSpace(parts[0])
				if clientIP != "" {
					_, port, _ := net.SplitHostPort(r.RemoteAddr)
					if port == "" {
						port = "0"
					}
					r.RemoteAddr = net.JoinHostPort(clientIP, port)
				}
			}
			if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
				r.URL.Scheme = strings.ToLower(proto)
			}

			if pc.ExternalAccess != nil && pc.ExternalAccess() == "agent" {
				if !isAgentPath(r.URL.Path) {
					http.Error(w, "Forbidden — this endpoint is not available externally", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isAgentPath returns true for paths that agents need: the agent API,
// payload downloads, and the health check.
func isAgentPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/agent/") ||
		strings.HasPrefix(path, "/payload/") ||
		path == "/healthz"
}

func parseCIDRs(s string) []*net.IPNet {
	if s == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		if !strings.Contains(cidr, "/") {
			ip := net.ParseIP(cidr)
			if ip == nil {
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			cidr = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, ipnet)
	}
	return nets
}

func isTrusted(remoteAddr string, nets []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
