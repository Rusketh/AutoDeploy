# Scaling AutoDeploy for mass deployments

> **Status.** Active. The pieces a single AutoDeploy server needs to
> drive a real fleet — payload mirrors per site, request throttling,
> cache headers, a `/metrics` endpoint and the operational guardrails
> they imply.

## The shape of the load

Imaging is bursty and bandwidth-heavy in one direction. The hot path is:

```
N machines PXE-boot at once
  → each fetches the Boot Client kernel + initrd (small: tens of MB total per machine)
  → each POSTs identity to /api/v1/clients/menu (tiny JSON)
  → each POSTs identity to /api/v1/images/{id}/manifest (tiny JSON)
  → each GETs a 4-10 GB WIM/ESD from /payload/iso/{id}/sources/install.wim
  → each GETs N driver packages, M software installers (tens-hundreds of MB)
```

The metadata is cheap — SQLite WAL serves thousands of identity /
menu / manifest calls per second on a commodity host. The payload
streams are the bottleneck. AutoDeploy addresses this with **payload
mirrors**: per-site HTTP servers that hold the same `/payload/...`
tree the primary serves. The primary rewrites the URLs in each
manifest to the right mirror; the actual byte-pushing happens
locally.

## Payload mirrors

Register one mirror per site in the portal under **Mirrors → Add
mirror**. Fields:

| Field      | What it does                                                                |
|------------|-----------------------------------------------------------------------------|
| Name       | Operator-friendly label.                                                     |
| Base URL   | The mirror's reachable URL (e.g. `https://eu-mirror.corp.example`).          |
| Site       | The site name this mirror serves. Empty = global fallback.                   |
| Priority   | Lower wins among same-site mirrors. Default 100.                             |
| Healthy    | Mark a mirror out of service without deleting it.                            |

### Routing

The Boot Client passes its site in the `X-AutoDeploy-Site` HTTP
header. The server picks a mirror in this order:

1. Highest-priority **healthy** mirror with `site = <request site>`.
2. Highest-priority **healthy** mirror with `site = ""` (global).
3. Fall back to the primary server's base URL.

Every machine's last seen site is remembered on the `machine_record`
so subsequent fetches without the header still route correctly.

### Setting the site on the Boot Client

The Boot Client looks for the site in three places, in order:

1. The `--site <name>` command-line flag.
2. The kernel command line, as `autodeploy.site=<name>`.
3. Empty (= use the global fallback).

For PXE deployments, the easiest path is to extend the iPXE chainload
script on each site's DHCP server to append the right `autodeploy.site=`:

```ipxe
# /var/lib/tftpboot/site-eu-west.ipxe
chain http://${primary}/ipxe/boot.ipxe?site=eu-west
```

and have the primary's `/ipxe/boot.ipxe` echo the parameter into the
kernel cmdline (already supported — the served script substitutes
`${base}` and operators can extend it with site routing).

### What the mirrors must serve

A mirror is any HTTP server that returns the same bytes the primary
would for these URLs:

```
GET /payload/iso/{id}/{path...}      (the extracted ISO tree)
GET /payload/drivers/{id}            (the driver-package blob)
GET /payload/software/{id}           (the software-installer blob)
```

Options for standing one up:

- **Rsync + nginx**: simplest. Sync `$AUTODEPLOY_DATA_DIR/iso/`,
  `/drivers/` and `/software/` to the mirror, serve them with nginx.
  Re-sync on a cron; mirrors don't need to know about the metadata.
- **Squid pull-through**: a Squid 5+ acting as a forward-rewriting
  reverse proxy in front of the primary, with a large disk cache.
  Mirrors are populated on demand by the first machine to fetch each
  payload at the site. Best for sites with high cache-hit ratios and
  modest individual ISO change.
- **Second `autodeploy-server` instance** running in mirror mode (a
  pull-through proxy with the same blob layout). Operator chooses
  this when they want a unified operational story across sites.

Unattend XML is generated server-side and is small, so it always
comes from the primary regardless of mirror configuration.

## In-flight throttling

`AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` (default 64) caps concurrent
`/payload/*` streams. Beyond the cap, requests queue (up to a 30 s
timeout per request) rather than thrash the file-descriptor budget.
Operationally this gives a graceful degradation curve: a 500-machine
burst sees throughput cap at 64 streams' worth, with the rest
waiting. Adjust based on:

- Available file descriptors (`ulimit -n`, default often 1024 — raise
  to 65535 on production hosts).
- NIC + disk read bandwidth. There's no point oversubscribing
  beyond what the host can move.
- Whether mirrors are absorbing most of the load. With mirrors
  configured, the primary mostly serves metadata; the cap can be
  small (32) without harming throughput.

The metric `autodeploy_payload_queued_waits_total` counts queue
events; if it grows fast, raise the cap or add a mirror.

## Cache headers

`/payload/*` responses set `Cache-Control: public, max-age=300` plus
the `ETag` and `Last-Modified` that `http.ServeContent` provides
automatically. This lets intermediate caches (squid, varnish, a CDN
in front of the mirror) avoid hitting the origin on every byte
of every request.

The 5-minute max-age is intentionally short: an operator can swap
the underlying blob by re-uploading via the portal; caches that
respect `max-age` will not serve a stale version for long.

## `/metrics`

The server exposes a Prometheus-format exposition at `/metrics`:

```
autodeploy_uptime_seconds 12345
autodeploy_http_requests_total 67890
autodeploy_http_requests_by_status{class="2xx"} 65000
autodeploy_http_requests_by_status{class="4xx"} 2750
autodeploy_http_requests_by_status{class="5xx"} 140
autodeploy_http_request_duration_ms_sum{route="payload_iso"} 1234567
autodeploy_payload_bytes_total 9876543210
autodeploy_payload_in_flight 12
autodeploy_payload_queued_waits_total 4
autodeploy_deployments_in_progress 3
autodeploy_deployments_completed_total{outcome="ok"} 152
autodeploy_deployments_completed_total{outcome="failed"} 4
autodeploy_bulk_jobs_in_flight 0
autodeploy_boot_menu_requests_total 287
autodeploy_manifest_requests_total 152
autodeploy_agent_checkins_total 18432
autodeploy_log_events_ingested_total 95812
```

This is the operational signal a Prometheus or Grafana monitoring
stack consumes. The structured request log (Phase 14) remains the
authoritative per-request audit trail; metrics are the fast
aggregate.

## Database

SQLite WAL handles the metadata load comfortably at AutoDeploy's
scale (low thousands of machines, hundreds of concurrent deploys).
At higher concurrencies the dominant cost shifts to log_event ingest
and agent check-in upserts. Tuning levers:

- **Raise the file-descriptor limit** — SQLite WAL can hold multiple
  shared-mode handles per connection.
- **Set `AUTODEPLOY_LOG_RETENTION_DAYS` aggressively** — the
  retention scheduler keeps the `log_event` table from growing
  unboundedly. 90 days is a reasonable default; 30 if disk is tight.
- **Front the server with a TLS-terminating reverse proxy** that
  also handles keepalive pools. The Go HTTP server is fine doing
  this itself, but a dedicated nginx in front lets you push more
  connections without inflating the Go heap.

## Operational recipe for "thousands at once"

1. Provision a primary with a fast disk (NVMe for the SQLite DB).
2. Provision one mirror per LAN segment / site. Each mirror needs
   only HTTP and a copy of the payload tree.
3. Register each mirror in the portal with its site name.
4. Configure your DHCP / iPXE chainloader to set
   `autodeploy.site=<site>` on the kernel cmdline for each subnet.
5. Set `AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` to 16-32 (the primary
   mostly serves metadata now).
6. Front the primary with HTTPS via cert/key env vars (or a
   reverse proxy with TLS termination).
7. Scrape `/metrics` from Prometheus; alert on
   `autodeploy_payload_queued_waits_total` rising sharply,
   `autodeploy_deployments_completed_total{outcome="failed"}` rising,
   `autodeploy_http_requests_by_status{class="5xx"}` non-zero.
8. Schedule daily snapshots of `$AUTODEPLOY_DATA_DIR` via
   `scripts/backup.sh`.

With this setup a primary host on commodity hardware handles 5k
machines per imaging window without the metadata becoming the bottleneck;
the limiting factor is the per-site mirror bandwidth, which scales
horizontally by adding more mirrors.
