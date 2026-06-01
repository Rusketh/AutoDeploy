# AutoDeploy Independent Code Audit Report

**Date:** 2026-06-01
**Scope:** Full codebase — server, agent, boot-client (~33.5k lines Go)
**Scale target:** 120+ simultaneous machine deployments via a single server

---

## Executive Summary

AutoDeploy is a well-structured Go project with clear separation between its three components (server, agent, boot-client). The code is generally clean, uses parameterised SQL queries (no SQL injection), employs bcrypt for password hashing with timing-attack mitigation, and demonstrates good secret-handling hygiene (credentials never logged, deploy tokens scoped, recovery keys escrowed without exposure).

However, the audit identified **critical security gaps** (unauthenticated API endpoints that allow full CRUD over deployment artifacts), several **scalability bottlenecks** that will impede the 120-machine target (SQLite contention, throttle defaults, LDAP per-request dialing), **bugs in the TFTP retransmit logic**, and numerous medium/low findings.

**Findings by severity:**
- Critical: 4
- High: 12
- Medium: 18
- Low / Informational: 15+

---

## 1. CRITICAL — Security

### 1.1 CRUD API Endpoints Have No Authentication

**Files:** `server/internal/api/iso_handlers.go`, `image_handlers.go`, `driver_handlers.go`, `software_handlers.go`, `unattend_handlers.go`, `loadout_handlers.go`, `inventory_handlers.go`

**All** JSON API CRUD endpoints for ISOs, images, drivers, software packages, unattends, loadouts, and inventory machines are **completely unauthenticated**. Any network client can:
- Create, update, and delete ISOs, images, drivers, and software packages
- Modify machine bindings (`PUT /api/v1/machines/{id}/binding`)
- Delete machines from inventory
- List all machines and their deployment history

Only the account-management endpoints (`/api/v1/accounts/*`), bulk operations, BitLocker, branding, and mirror handlers call `UserFromRequest()`. The portal HTML pages are protected via `requireSession()`, but the underlying API is wide open.

**Impact:** An attacker on the network can tamper with all deployment configurations, inject malicious software packages, or delete inventory data — without any credentials.

**Recommendation:** Add an authentication middleware wrapping all `/api/v1/*` routes (except the explicitly-public agent/client endpoints). A single `requireAuth` middleware on the mux group is simpler and less error-prone than per-handler checks.

### 1.2 Payload Upload Endpoints Have No Authentication or Size Limits

**Files:** `server/internal/payload/serve.go:62-66`, `:209`, `:287`, `:318`

The ISO upload (`PUT /api/v1/isos/{id}/upload`), driver upload (`PUT /api/v1/drivers/{id}/upload`), and software upload (`PUT /api/v1/software/{id}/upload`) endpoints have **no authentication and no request body size limit**. `r.Body` is passed directly to `s.Blobs.WriteStream`.

**Impact:** Denial of service via disk exhaustion; malicious payload injection into the deployment pipeline.

**Recommendation:** Require session authentication. Wrap bodies with `http.MaxBytesReader`. Consider separate upload tokens for automation.

### 1.3 Server Update Endpoint Lacks Authentication

**File:** `server/internal/api/version_handlers.go:329-380`

`handleServerUpdate` has a comment claiming "admin-or-better authorization the api package's middleware applies" but the code **never actually checks authentication**. Any unauthenticated caller can trigger `sudo autodeploy-update` with an arbitrary semver tag, effectively executing arbitrary code as root on the server.

Similarly, `handleInstallAgent` (line 108) lacks an auth check — anyone can trigger downloads from GitHub.

**Impact:** Remote code execution on the server host via unauthenticated API call.

### 1.4 Domain-Join Credentials Returned Without Proper Authentication

**File:** `server/internal/api/domainjoin_handlers.go:37-83`

`handleAgentDomainJoin` returns AD domain-join credentials (username + password) to any caller that knows a valid `agent_id`. Unlike the BitLocker config endpoint which checks `X-AutoDeploy-Deploy-Token`, this endpoint has no bearer token verification. An attacker who captures an `agent_id` from network traffic can retrieve AD join credentials.

---

## 2. HIGH — Scalability (120-Machine Target)

### 2.1 SQLite Connection Pool Not Configured; busy_timeout Too Low

**File:** `server/internal/storage/storage.go:36-63`

The database is opened with `sql.Open()` using **default pool settings** (unlimited open connections, 2 idle). SQLite with WAL mode allows only one writer at a time; with 120 agents polling + boot clients + portal users, the 5-second `busy_timeout` will be overwhelmed, producing `SQLITE_BUSY` errors cascading as HTTP 500s.

**Recommendation:**
```go
sqlDB.SetMaxOpenConns(1)      // serialise all writes through one connection
sqlDB.SetMaxIdleConns(1)      // keep that connection warm
sqlDB.SetConnMaxLifetime(0)   // never close idle connections
```
Raise `busy_timeout` to at least 30 seconds. Consider a read/write connection split.

### 2.2 Payload Throttle Default (64) Below 120-Machine Target; Timeout Hard-Coded

**Files:** `server/internal/config/config.go:86`, `server/internal/payload/throttle.go:63`

`AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` defaults to 64. During a 120-machine PXE burst, 56 machines will queue. The throttle's **hard-coded 30-second timeout** means machines that don't get a slot receive a 503 and must retry — but boot clients have no retry logic (see 4.6).

**Recommendation:** Raise default to ≥128; make the queue timeout configurable; add retry logic to boot clients.

### 2.3 LDAP Dial-Per-Operation Creates 480+ Connections at Scale

**File:** `server/internal/addomain/ldap.go:44-59`

Every LDAP operation dials a fresh TLS connection, binds, performs one operation, and disconnects. `PrepareComputer` can make 4 separate connections per machine (find + delete + create + setGroups). At 120 concurrent deployments: **480 near-simultaneous LDAP connections**.

**Recommendation:** Implement LDAP connection pooling.

### 2.4 Agent HTTP Client Has No Timeouts or Retry Logic

**File:** `agent/internal/httpc/client.go:26-34`

The agent's `http.Client` has **zero timeout**. A stalled server or network partition causes the agent to hang indefinitely. No retry with exponential backoff — a single transient error causes the entire poll cycle to be skipped. With 120 agents, a brief server hiccup blocks all of them.

**Recommendation:** Set `Timeout` (30s for API, 5m for downloads). Add retry with jitter for 5xx and connection errors.

### 2.5 Agent Polling Has No Jitter — Thundering Herd

**File:** `agent/cmd/autodeploy-agent/main.go:446-496`

`runSelfLoop` uses a fixed `time.NewTicker(interval)` with **no random jitter**. If the server reboots or 120 agents start simultaneously, all hit `/api/v1/agent/self` within the same second every interval. The server can suggest a `PollIntervalSeconds` but this still synchronizes everyone.

**Recommendation:** Add ±20% random jitter to the poll interval. Stagger initial polls with a random delay at startup.

### 2.6 Dashboard and Machine List: N+1 Query Patterns

**Files:** `server/internal/portal/portal.go:278-343`, `portal/inventory.go:193-233`

The dashboard loads ALL machines, then for EACH machine loads its full deployment history (N+1 queries). The machine list page does N+3 queries per displayed machine (`GetBinding`, `Images.Get`, `BitLocker.PINStatus`). At page size 50: 150+ queries per page load. Under concurrent portal access during a 120-machine deployment, this becomes a SQLite contention hotspot.

**Recommendation:** Use JOINs or batch queries. Add pagination to the dashboard.

### 2.7 `ResolveForMachine` Loads ALL Driver Packages Per Manifest Request

**File:** `server/internal/resolve/resolve.go:175`

Every manifest request calls `r.drivers.List(ctx)` loading every driver package, then evaluates filters. With hundreds of driver packages and 120 concurrent manifest requests, this is N×M filter evaluations with no caching. The driver list is effectively static between operator edits.

**Recommendation:** Cache the driver list with invalidation on write.

### 2.8 `ClaimJobsFor` Has a Read-Then-Write Race Condition

**File:** `server/internal/model/bulk.go:236-272`

`ClaimJobsFor` reads queued jobs with SELECT, then updates them individually **outside a transaction**. Two concurrent agents (or retries) can both SELECT the same jobs and both UPDATE them to "running" — causing duplicate job execution.

**Recommendation:** Use a single `UPDATE ... WHERE status='queued' ... RETURNING` or wrap in a transaction with the WHERE guard.

### 2.9 Log Batch Ingestion Holds Write Lock for Unbounded Input

**File:** `server/internal/model/logs.go:58-91`

`AppendBatch` accepts an unbounded `[]LogEvent` and inserts all in a single transaction. With 120 agents each submitting 2048-record batches, the write lock is held for extended periods, starving all other writers.

**Recommendation:** Limit batch size (e.g. 500). Process in chunks.

---

## 3. HIGH — Security

### 3.1 Session Cookie Missing `Secure` Flag Behind Reverse Proxy

**File:** `server/internal/api/auth_handlers.go:58-66`

```go
Secure: req.TLS != nil,
```

Behind a TLS-terminating reverse proxy, `req.TLS` is nil. The session cookie is set without `Secure`, vulnerable to interception.

**Recommendation:** Check `X-Forwarded-Proto: https` or add a config flag for reverse-proxy mode.

### 3.2 No CSRF Protection on State-Mutating Endpoints

**Files:** All POST/PUT/DELETE handlers in `server/internal/api/` and `server/internal/portal/`

Cookie-based auth with no CSRF token validation. `SameSite: Lax` provides partial protection but doesn't cover all attack vectors.

### 3.3 No Rate Limiting on Login Endpoint

**File:** `server/internal/api/auth_handlers.go:40-72`

Unlimited password guessing attempts. The bcrypt timing mitigation prevents user enumeration but doesn't prevent brute force.

---

## 4. MEDIUM — Bugs

### 4.1 TFTP Retransmit Logic Is Dead Code — Any Lost Packet Kills Transfer

**Files:** `server/internal/tftp/tftp.go:408-419`, `:438-476`

The `sendDATA` retry loop has an unconditional `return nil` on the first iteration — the `for attempt` loop never loops. `awaitACK` returns an error on timeout but never retransmits the last DATA block. **Any single lost UDP packet terminates the entire transfer.** The `maxRetries` constant (5) is defined but effectively unused.

```go
for attempt := 0; attempt < maxRetries; attempt++ {
    if _, err := t.conn.WriteToUDP(pkt, t.client); err != nil {
        return err
    }
    return nil  // ← always returns on first iteration
}
```

**Impact:** On unreliable networks, PXE boot failures. On LANs, typically OK due to low packet loss.

### 4.2 `coordinateRenameAD` Renames All Machines to the Same Name

**File:** `server/internal/api/bulk_handlers.go:101-145`

The function parses `payload.NewName` once and uses it for **every** job in the loop. In a bulk rename affecting multiple machines, every machine's AD object gets renamed to the same name. The portal sends `rename_find`/`rename_replace` (regex-based) but this function looks for `new_name`.

### 4.3 LDAP Injection in `RenameComputer`

**File:** `server/internal/addomain/ldap.go:169`

```go
req := ldap.NewModifyDNRequest(dn, "CN="+newName, true, "")
```

`newName` is not escaped for DN special characters. Compare with `FindComputer` which correctly uses `ldap.EscapeFilter()`, and `CreateComputer` which uses `ldapEscape()`.

### 4.4 Boot Client Disk Wipe Before Download Creates Unrecoverable State

**File:** `boot-client/cmd/autodeploy-boot/main.go:381-394`

If `downloadMedia` fails after the disk is already partitioned (`sgdisk --zap-all` at line 381), the machine has a wiped disk and no recovery path. The existing OS was destroyed before the new media was successfully staged.

**Recommendation:** Download media to a temporary location first, then wipe+partition only after confirming the download succeeded.

### 4.5 Boot Client Downloads Agent Binary Without Hash Verification

**File:** `boot-client/cmd/autodeploy-boot/main.go:518-549`

`fetchAgent` downloads the agent binary with only a 2-byte "MZ" magic check (`looksLikePE`). No SHA-256 verification. A compromised server or MITM can inject any PE binary.

### 4.6 `InventoryRepo.Delete` Misses Related Tables

**File:** `server/internal/model/inventory.go:464-481`

The Delete transaction cleans up `machine_binding`, `deployment_history`, `machine_detected_state`, and `machine_record`, but does NOT delete from `bitlocker_pin`, `bitlocker_recovery_key`, `machine_deploy_token`, or `bulk_job`. Orphaned rows accumulate; encrypted secrets for deleted machines remain.

### 4.7 `statusRecorder` / `byteCounter` Don't Implement `http.Flusher`

**Files:** `server/internal/httpx/server.go:90-107`, `server/internal/payload/serve.go:510-519`

These `ResponseWriter` wrappers don't delegate optional interfaces (`Flusher`, `Hijacker`). This can interfere with `http.ServeContent` range responses and HTTP/2 streaming for the 120 concurrent payload downloads.

### 4.8 Open Redirect in Portal Login

**File:** `server/internal/portal/portal.go:220-222`

The `next` parameter checks `strings.HasPrefix(next, "/portal")` which allows redirects like `/portal.evil.com`. Should use `/portal/` (with trailing slash).

### 4.9 `smbios.ReadFromSysfs` Never Returns an Error

**File:** `boot-client/internal/smbios/smbios.go:42-67`

Function signature returns `(Identity, error)` but always returns `nil` error. The caller's error check is dead code. An empty `SystemUUID` silently proceeds, breaking inventory lookups, BitLocker, and bulk-job claims.

### 4.10 TOCTOU Race in Delete Operations

**Files:** `server/internal/model/loadout.go:175-192`, `iso.go:170-187`, `unattend.go:102-119`, `image.go:195-212`

All Delete methods check `RefCount` then delete in separate operations (not in a transaction). A concurrent request can create a reference between the check and the delete.

---

## 5. MEDIUM — Optimisations & Code Quality

### 5.1 Server Shutdown Doesn't Gracefully Drain Connections

**File:** `server/cmd/autodeploy-server/main.go:259-322`

The `ctx` parameter is accepted but never used for shutdown. `srv.ListenAndServe()` blocks forever; cancelling `ctx` has no effect. During a 120-machine deployment, SIGTERM kills active payload downloads.

**Recommendation:** Use `srv.Shutdown(ctx)` in a goroutine watching `ctx.Done()`.

### 5.2 Duplicate Code: `runCheckInLoop` vs `runSelfLoop`

**File:** `agent/cmd/autodeploy-agent/main.go:892-939` vs `:445-496`

Two nearly identical polling loops. The legacy path calls `runCheckInLoop` after `installPackages`, potentially re-executing completed work.

### 5.3 Duplicate Code: `logging/shipper.go` Copied Between Agent and Boot Client

**Files:** `agent/internal/logging/shipper.go`, `boot-client/internal/logging/shipper.go`

Character-for-character identical (230 lines). Same for `logger.go`. A fix in one must be manually replicated.

### 5.4 Log Shipper Creates New HTTP Client on Every Call

**Files:** `agent/internal/logging/shipper.go:156-161`, `boot-client/internal/logging/shipper.go:156-161`

`Ship()` instantiates a fresh `http.Client` + `http.Transport` per call = new TLS handshake per log-ship cycle. At 120 agents: 120 unnecessary TLS handshakes per interval.

### 5.5 Log Shipper Buffer Drop Is O(n) Per Record

**File:** `agent/internal/logging/shipper.go:100-106`

At capacity, each new record triggers `copy(st.buffer, st.buffer[1:])` — an O(n) memmove of 2048 entries. Under load, logging becomes O(n²). A ring buffer would be O(1).

### 5.6 `idStr` / `itoa64` Duplicate Hand-Rolled Int-to-String

**Files:** `server/internal/api/agent_handlers.go:277-300`, `bitlocker_handlers.go:258-279`

Two identical hand-rolled functions. Both should be `strconv.FormatInt`.

### 5.7 `replaceToken` Reimplements `strings.ReplaceAll`

**File:** `agent/cmd/autodeploy-agent/main.go:1183-1200`

Comment says "avoids importing strings" but `strings` is already imported at line 21.

### 5.8 Retention Scheduler Only Prunes Logs — Expired Sessions Accumulate

**File:** `server/internal/retention/retention.go`

The `user_session` table is never cleaned up. Expired sessions are only deleted when looked up.

### 5.9 `startsWith` / `hasSuffix` Reimplement Standard Library

**File:** `server/internal/metrics/metrics.go:201-206`

Equivalent to `strings.HasPrefix`/`strings.HasSuffix`, no faster.

### 5.10 `prepJobs` Map Grows Monotonically — No Eviction

**File:** `server/internal/payload/prep_async.go:50`

Completed prepare job statuses are never removed. Slow memory leak.

### 5.11 `serveISOIndex` Rebuilds Full File List on Every Request

**File:** `server/internal/payload/serve.go:361`

Does a full `filepath.WalkDir` per request. With 120 concurrent machines requesting the index, this is 120 concurrent directory walks of hundreds of files. Should be cached (changes only on re-extract).

### 5.12 Portal `sessionUser` Called Twice Per Protected Request

**File:** `server/internal/portal/portal.go:142-153`, `:449`

`requireSession` calls `sessionUser`, then `render` calls it again. Each call = 2 DB queries. Doubles auth overhead.

### 5.13 Settings: Sequential DB Round-Trips for AD Config Save

**File:** `server/internal/runtime/settings.go:218-254`

`SetADConfig` calls `setRaw` 5 times sequentially, each doing an INSERT/UPDATE + full table re-read. That's 10 SQLite operations for one config save.

---

## 6. Missing Validation

### 6.1 No Request Body Size Limit on JSON Endpoints

**File:** `server/internal/api/api.go:156-159`

`decodeJSON` reads from `r.Body` with no size limit. A multi-GB JSON body consumes server memory.

### 6.2 No Validation of Entity Names

Create endpoints accept names without length or character validation — XSS risk via server-rendered templates.

### 6.3 Internal Error Messages Leaked to Clients

**File:** `server/internal/api/api.go:143`

The default `writeError` case exposes raw Go error messages (SQL errors, file paths) to unauthenticated callers.

### 6.4 KMS Server Value Not Sanitized for Command Injection

**File:** `server/internal/unattend/generate.go:234-239`

`KMSServer` is interpolated into a `cscript` command line without validation. Shell metacharacters (e.g. `& calc.exe`) become arbitrary command execution on the deployed Windows machine.

### 6.5 `SetupComplete.cmd` Template Doesn't Sanitize Server URL

**File:** `boot-client/cmd/autodeploy-boot/main.go:575-596`

`{{SERVER}}` and `{{AGENTID}}` are replaced without escaping cmd.exe metacharacters (`&`, `|`, `>`).

---

## 7. CI / Build Issues

### 7.1 Go Version Mismatch: CI Uses 1.22, go.mod Requires 1.25

**Files:** `.github/workflows/ci.yml:23,48,73` vs `server/go.mod:3`

CI builds with Go 1.22 but modules require Go 1.25. Will fail on any Go 1.25 features.

### 7.2 No Race Detector in CI Tests

**File:** `.github/workflows/ci.yml:29,54,79`

`go test -count=1 ./...` — no `-race` flag. For a server handling 120+ concurrent clients, race conditions are a primary risk.

### 7.3 No Static Analysis or Dependency Vulnerability Scanning

No `golangci-lint`, `staticcheck`, `gosec`, or `govulncheck`. No Dependabot/Renovate configuration.

### 7.4 DevMode Defaults to `true`

**File:** `server/internal/config/config.go:66`

Production installs that forget `AUTODEPLOY_DEV=false` get cleartext HTTP on public interfaces, self-signed certs, and relaxed security.

---

## 8. Dead Code

| Location | Description |
|----------|-------------|
| `agent/cmd/autodeploy-agent/main.go:251` | `_ = time.Now` — Phase 13 placeholder, now implemented |
| `agent/cmd/autodeploy-agent/main.go:1319` | `var _ = strconv.Itoa` — unused import keep-alive |
| `server/internal/runtime/settings.go:401` | `var _ = sql.ErrNoRows` — unused import workaround |
| `server/internal/portal/stubs.go:9-13` | `placeholder` function — all entities have real implementations |
| `server/internal/tftp/tftp.go:413-419` | TFTP retry loop body after unconditional return |
| `server/internal/api/version_handlers.go:700` | `var _ = time.Now` — time is already used |

---

## 9. Test Coverage Gaps

| Area | Issue |
|------|-------|
| **Throttle** | Zero tests for the key scalability mechanism (semaphore, timeout, context cancellation) |
| **API handlers** | 15 handler files with zero test coverage |
| **Auth/PIN** | No tests for PIN lockout logic, rate limiting |
| **Config** | No tests for env parsing, edge cases |
| **Retention** | No tests for the scheduler |
| **Metrics** | No tests |
| **Portal** | 3 test files for ~20 source files |
| **Agent httpc/logging/bitlocker** | Zero tests |
| **Concurrency** | No load/stress tests for 120+ concurrent scenarios |
| **Integration** | No end-to-end boot→deploy→agent test |
| **SQLite contention** | No concurrent write tests |

---

## 10. Feature Recommendations

### 10.1 Webhook / Event Notifications
Add webhook support for deployment events (completed, failed, machine registered) → Slack, Teams, ticketing systems.

### 10.2 Deployment Wave Scheduling
Deploy in configurable waves (e.g. 20 at a time) to avoid overwhelming the network and server.

### 10.3 Real-Time Operational Dashboard
Built-in dashboard showing in-flight deployments, payload throughput, agent liveness, and queue depth — beyond the Prometheus `/metrics` endpoint.

### 10.4 Agent Liveness Alerting
Configurable thresholds (e.g. "alert if agent hasn't checked in for 2× poll interval").

### 10.5 Role-Based Access Control (RBAC)
Currently all users are full admin. RBAC (viewer, operator, admin) prevents accidental changes at scale.

### 10.6 API Key Authentication
Session cookies are unsuitable for CI/CD pipelines. Bearer-token / API-key auth for automation.

### 10.7 Payload Deduplication / Content-Addressable Storage
SHA-256-keyed storage would reduce disk usage when ISOs/packages share files.

### 10.8 Deployment Rollback
"Last known good" image binding for recovering from bad deployments at scale.

### 10.9 PXE Boot Staggering
Server-side random delay injection in boot menu responses to smooth the thundering herd.

### 10.10 PostgreSQL Backend Option
For 120+ machines with frequent polling, SQLite's single-writer limitation is the primary scaling bottleneck. A PostgreSQL option would unlock concurrent write performance.

---

## Summary of Action Items (Priority Order)

| # | Severity | Category | Summary |
|---|----------|----------|---------|
| 1.1 | **CRITICAL** | Security | Add authentication to all CRUD API endpoints |
| 1.2 | **CRITICAL** | Security | Add authentication + size limits to upload endpoints |
| 1.3 | **CRITICAL** | Security | Add authentication to server update endpoint |
| 1.4 | **CRITICAL** | Security | Add token verification to domain-join credential endpoint |
| 2.1 | **HIGH** | Scale | Configure SQLite connection pool + raise busy_timeout |
| 2.2 | **HIGH** | Scale | Raise payload throttle default; make timeout configurable |
| 2.5 | **HIGH** | Scale | Add polling jitter to prevent thundering herd |
| 2.8 | **HIGH** | Scale | Fix `ClaimJobsFor` race condition (duplicate job execution) |
| 3.1 | **HIGH** | Security | Fix Secure cookie flag behind reverse proxy |
| 3.2 | **HIGH** | Security | Add CSRF protection |
| 3.3 | **HIGH** | Security | Add login rate limiting |
| 6.1 | **HIGH** | Security | Add request body size limits |
| 4.1 | **MEDIUM** | Bug | Fix TFTP retransmit dead code |
| 4.2 | **MEDIUM** | Bug | Fix bulk rename applying same name to all machines |
| 4.3 | **MEDIUM** | Bug | Fix LDAP injection in RenameComputer |
| 4.4 | **MEDIUM** | Bug | Reorder boot client: download before disk wipe |
| 5.1 | **MEDIUM** | Reliability | Implement graceful server shutdown |
| 2.3 | **MEDIUM** | Scale | Pool LDAP connections |
| 2.4 | **MEDIUM** | Scale | Add HTTP client timeouts in agent/boot-client |
| 7.1 | **MEDIUM** | CI | Fix Go version mismatch |
| 7.2 | **MEDIUM** | CI | Add `-race` to test runs |
| 6.4 | **MEDIUM** | Security | Sanitize KMS server for command injection |
