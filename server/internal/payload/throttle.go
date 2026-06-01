package payload

import (
	"net/http"
	"sync/atomic"
	"time"
)

// Throttle bounds the number of concurrent /payload/* requests in
// flight. A semaphore with capacity N: when full, subsequent requests
// block until a slot frees (or the request context is cancelled).
//
// Purpose: with 500 machines PXE-booting simultaneously and pulling a
// 5 GB WIM, the server's file-descriptor and socket-buffer budgets
// matter. Better to queue and serve in waves than to thrash and drop
// connections.
type Throttle struct {
	slots chan struct{}
	// metrics hook
	onWait       func()
	inFlight     atomic.Int64
	queueTimeout time.Duration
}

// NewThrottle returns a Throttle with capacity slots. capacity<=0
// returns a no-op throttle (unbounded). A zero queueTimeout defaults
// to 2 minutes, which is more reasonable for 120+ machines pulling
// multi-GB files.
func NewThrottle(capacity int, queueTimeout time.Duration, onWait func()) *Throttle {
	if queueTimeout <= 0 {
		queueTimeout = 2 * time.Minute
	}
	t := &Throttle{onWait: onWait, queueTimeout: queueTimeout}
	if capacity > 0 {
		t.slots = make(chan struct{}, capacity)
	}
	return t
}

// InFlight returns the live count.
func (t *Throttle) InFlight() int64 { return t.inFlight.Load() }

// Wrap returns h with throttle accounting. When throttling is enabled
// it acquires a slot before calling h and releases on return.
func (t *Throttle) Wrap(h http.Handler) http.Handler {
	if t.slots == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.inFlight.Add(1)
			defer t.inFlight.Add(-1)
			h.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to acquire without blocking first; only count a "wait"
		// if we actually had to wait.
		select {
		case t.slots <- struct{}{}:
		default:
			if t.onWait != nil {
				t.onWait()
			}
			// Block but honour the request's context (a Boot Client
			// that gives up should free its slot for the next).
			select {
			case t.slots <- struct{}{}:
			case <-r.Context().Done():
				http.Error(w, "request cancelled while queued", http.StatusServiceUnavailable)
				return
			case <-time.After(t.queueTimeout):
				http.Error(w, "payload throttle queue timed out", http.StatusServiceUnavailable)
				return
			}
		}
		t.inFlight.Add(1)
		defer func() {
			t.inFlight.Add(-1)
			<-t.slots
		}()
		h.ServeHTTP(w, r)
	})
}

// WrapFunc is the http.HandlerFunc variant.
func (t *Throttle) WrapFunc(h http.HandlerFunc) http.HandlerFunc {
	wrapped := t.Wrap(h)
	return func(w http.ResponseWriter, r *http.Request) { wrapped.ServeHTTP(w, r) }
}
