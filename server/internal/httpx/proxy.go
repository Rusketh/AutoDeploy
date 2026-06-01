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

// DynamicTrustedProxyMiddleware is like TrustedProxyMiddleware but
// re-reads the CIDR list on every request via the provided function.
// This lets the operator change trusted proxies at runtime without a
// restart.
func DynamicTrustedProxyMiddleware(cidrsFn func() string, next http.Handler) http.Handler {
	var (
		mu       sync.RWMutex
		lastCIDR string
		cached   []*net.IPNet
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := cidrsFn()
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
		if len(nets) > 0 && isTrusted(r.RemoteAddr, nets) {
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
