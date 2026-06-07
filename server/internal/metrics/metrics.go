// Package metrics is the tiny Prometheus-format exposition the server
// exposes at /metrics. We deliberately avoid the official Prometheus
// client library: it pulls in a substantial dependency for a handful of
// counters, and the exposition format is a few text lines.
//
// Counters and gauges are atomic int64; histogram-style "buckets"
// aren't used here. The server logs full request data structurally; the
// metrics are a smaller fast-loop for scrape-based monitoring.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing integer.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Add(n int64) { c.v.Add(n) }
func (c *Counter) Inc()        { c.v.Add(1) }
func (c *Counter) Get() int64  { return c.v.Load() }

// Gauge is an integer that may go up or down.
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Set(n int64) { g.v.Store(n) }
func (g *Gauge) Inc()        { g.v.Add(1) }
func (g *Gauge) Dec()        { g.v.Add(-1) }
func (g *Gauge) Get() int64  { return g.v.Load() }

// Registry holds the named counters and gauges the server emits.
type Registry struct {
	StartTime time.Time

	HTTPRequestsTotal    *Counter
	HTTPRequestsByStatus *labeledCounter
	HTTPRequestDuration  *labeledCounter // sum of milliseconds — coarse but useful

	PayloadBytesServed *Counter
	PayloadInFlight    *Gauge
	PayloadQueuedWaits *Counter // increments each time a request had to wait on the semaphore

	DeploymentsInProgress *Gauge
	DeploymentsCompleted  *labeledCounter // by outcome
	BulkJobsInFlight      *Gauge

	BootMenuRequests  *Counter
	ManifestRequests  *Counter
	AgentCheckins     *Counter
	LogEventsIngested *Counter
}

// New returns a Registry with all counters zeroed.
func New() *Registry {
	return &Registry{
		StartTime:             time.Now(),
		HTTPRequestsTotal:     &Counter{},
		HTTPRequestsByStatus:  newLabeledCounter(),
		HTTPRequestDuration:   newLabeledCounter(),
		PayloadBytesServed:    &Counter{},
		PayloadInFlight:       &Gauge{},
		PayloadQueuedWaits:    &Counter{},
		DeploymentsInProgress: &Gauge{},
		DeploymentsCompleted:  newLabeledCounter(),
		BulkJobsInFlight:      &Gauge{},
		BootMenuRequests:      &Counter{},
		ManifestRequests:      &Counter{},
		AgentCheckins:         &Counter{},
		LogEventsIngested:     &Counter{},
	}
}

// labeledCounter is a tiny map[label]*Counter wrapper.
type labeledCounter struct {
	mu sync.Mutex
	m  map[string]*Counter
}

func newLabeledCounter() *labeledCounter { return &labeledCounter{m: map[string]*Counter{}} }

func (l *labeledCounter) Add(label string, n int64) {
	l.mu.Lock()
	c, ok := l.m[label]
	if !ok {
		c = &Counter{}
		l.m[label] = c
	}
	l.mu.Unlock()
	c.Add(n)
}

func (l *labeledCounter) Inc(label string) { l.Add(label, 1) }

func (l *labeledCounter) snapshot() map[string]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int64, len(l.m))
	for k, c := range l.m {
		out[k] = c.Get()
	}
	return out
}

// Handler returns an http.Handler that writes the Prometheus exposition.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writeCounter(w, "autodeploy_uptime_seconds", "Server uptime in seconds.", int64(time.Since(r.StartTime).Seconds()))
		writeCounter(w, "autodeploy_http_requests_total", "Total HTTP requests.", r.HTTPRequestsTotal.Get())
		writeLabeled(w, "autodeploy_http_requests_by_status", "HTTP requests by status class (2xx/4xx/5xx).", r.HTTPRequestsByStatus.snapshot(), "class")
		writeLabeled(w, "autodeploy_http_request_duration_ms_sum", "Sum of request durations in ms by route bucket.", r.HTTPRequestDuration.snapshot(), "route")
		writeCounter(w, "autodeploy_payload_bytes_total", "Total bytes served from /payload/*.", r.PayloadBytesServed.Get())
		writeGauge(w, "autodeploy_payload_in_flight", "Current /payload/* requests being served.", r.PayloadInFlight.Get())
		writeCounter(w, "autodeploy_payload_queued_waits_total", "Times a payload request waited on the throttle semaphore.", r.PayloadQueuedWaits.Get())
		writeGauge(w, "autodeploy_deployments_in_progress", "Deployments currently in_progress.", r.DeploymentsInProgress.Get())
		writeLabeled(w, "autodeploy_deployments_completed_total", "Deployments completed by outcome.", r.DeploymentsCompleted.snapshot(), "outcome")
		writeGauge(w, "autodeploy_bulk_jobs_in_flight", "Bulk jobs currently running on agents.", r.BulkJobsInFlight.Get())
		writeCounter(w, "autodeploy_boot_menu_requests_total", "Boot Client menu requests.", r.BootMenuRequests.Get())
		writeCounter(w, "autodeploy_manifest_requests_total", "Manifest endpoint requests.", r.ManifestRequests.Get())
		writeCounter(w, "autodeploy_agent_checkins_total", "Resident-agent check-ins.", r.AgentCheckins.Get())
		writeCounter(w, "autodeploy_log_events_ingested_total", "Log events ingested from clients.", r.LogEventsIngested.Get())
	}
}

func writeCounter(w io.Writer, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
}
func writeGauge(w io.Writer, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
}
func writeLabeled(w io.Writer, name, help string, m map[string]int64, labelName string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s=%q} %d\n", name, labelName, k, m[k])
	}
}

// CountStatus maps an HTTP status to "2xx"/"4xx"/"5xx".
func CountStatus(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	}
	return "1xx"
}

// BucketRoute collapses a path to a low-cardinality bucket for the
// duration counter. We do not want one metric per machine UUID.
func BucketRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/payload/iso/"):
		return "payload_iso"
	case strings.HasPrefix(path, "/payload/drivers/"):
		return "payload_drivers"
	case strings.HasPrefix(path, "/payload/software/"):
		return "payload_software"
	case strings.HasPrefix(path, "/payload/unattend/"):
		return "payload_unattend"
	case strings.HasPrefix(path, "/api/v1/agent/checkin"):
		return "agent_checkin"
	case strings.HasPrefix(path, "/api/v1/agent/report"):
		return "agent_report"
	case strings.HasPrefix(path, "/api/v1/agent/"):
		return "agent_other"
	case strings.HasPrefix(path, "/api/v1/clients/menu"):
		return "client_menu"
	case strings.HasPrefix(path, "/api/v1/clients/validate-pin"):
		return "client_pin"
	case strings.HasPrefix(path, "/api/v1/images/") && strings.HasSuffix(path, "/manifest"):
		return "manifest"
	case strings.HasPrefix(path, "/api/v1/logs/ingest"):
		return "logs_ingest"
	case strings.HasPrefix(path, "/api/v1/"):
		return "api_other"
	case strings.HasPrefix(path, "/portal/"):
		return "portal"
	case path == "/healthz":
		return "healthz"
	case path == "/metrics":
		return "metrics"
	}
	return "other"
}
