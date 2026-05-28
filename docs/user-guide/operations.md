# Operating AutoDeploy in production

> **Status.** Phase 16. The release-readiness pass: deployment topology,
> backup/recovery, log retention, performance, security review checklist.

## Deployment topology

The initial release is **single-server**. The architecture does not
preclude site servers — the API and payload service are stateless apart
from SQLite — but multi-site sharding is a future design (open question
#2 carried over from the design doc).

Recommended single-server layout:

```
  +-----------+
  |    DCs    |    DNS-based discovery — optional
  +-----------+

  +-----------+        +---------------------+
  |  Operator |  <---> |   AutoDeploy server |   :443 (HTTPS, portal + API)
  |  browser  |        |  + Domain Service   |   :80  (loopback, behind TLS terminator)
  +-----------+        +---------------------+
                            |  reads / writes
                            v
                     +-------------+
                     |   $DATA_DIR |    /var/lib/autodeploy
                     +-------------+
                          |
                          v
                +-----+  +-----+  +-----+
                | iso |  | drv |  | sw  |   payload blobs
                +-----+  +-----+  +-----+

  +-------------+      iPXE      +-----------+
  |   Target    |  ------------> |  /ipxe/   |  kernel + initrd
  |   machines  |  -----> /api -> AutoDeploy
  +-------------+
```

Place the server on a machine with:

- Reliable connectivity to every site the targets PXE-boot from.
- Disk capacity for ISO/driver/software payloads (50-500 GB depending on
  catalogue size).
- An LDAPS line of sight to the AD service account's DCs (if AD is used).

## Environment variables (full list)

| Variable                          | Purpose                                          |
|-----------------------------------|--------------------------------------------------|
| `AUTODEPLOY_HTTP_ADDR`            | Cleartext HTTP bind. Loopback only in prod.       |
| `AUTODEPLOY_HTTPS_ADDR`           | HTTPS bind.                                      |
| `AUTODEPLOY_TLS_CERT` / `_KEY`    | PEM cert + key for HTTPS (required in prod).     |
| `AUTODEPLOY_DATA_DIR`             | Persistent state root.                           |
| `AUTODEPLOY_DEV`                  | `false` in production.                           |
| `AUTODEPLOY_AD_URL`               | LDAP URL of the directory.                       |
| `AUTODEPLOY_AD_BIND_DN`           | Service-account DN.                              |
| `AUTODEPLOY_AD_BIND_PASSWORD`     | Service-account password. **Secret.**            |
| `AUTODEPLOY_AD_SEARCH_BASE`       | AD subtree the service operates within.          |
| `AUTODEPLOY_AD_SKIP_TLS_VERIFY`   | Lab use only.                                    |
| `AUTODEPLOY_SECRETS_KEY`          | Hex-encoded 32-byte at-rest encryption key.      |
| `AUTODEPLOY_LOG_RETENTION_DAYS`   | Auto-prune log_event older than N days. 0 = never.|

## Backup and recovery

A reference script lives at `scripts/backup.sh`:

```sh
AUTODEPLOY_DATA_DIR=/var/lib/autodeploy ./scripts/backup.sh /var/backups/autodeploy
# writes /var/backups/autodeploy/autodeploy-<timestamp>.tar.gz (mode 0600)
```

The archive contains a consistent SQLite snapshot, the at-rest
encryption key, the TLS material and the (one-time) bootstrap-admin
file if it still exists.

Payload blobs (`iso/`, `drivers/`, `software/`) are NOT in the archive —
they are large and re-uploadable. Take separate filesystem snapshots if
you need them.

**Restore**: stop the server, untar the archive into a fresh
`$AUTODEPLOY_DATA_DIR`, restart. Without the `secrets-key.bin` the
escrowed PINs and recovery keys are unreadable, so back this up
out-of-band.

## Log retention

Set `AUTODEPLOY_LOG_RETENTION_DAYS=90` (or whatever your policy
requires) and the server's hourly retention loop prunes
`log_event` rows older than the cutoff. The loop logs a
`retention.log_prune.ok` event with the row count it removed.

## Security review checklist

Done as part of Phase 16; rerun before each release.

- [ ] HTTPS enforced. The server refuses cleartext HTTP on non-loopback
      in production mode (Phase 0).
- [ ] Session cookies are `HttpOnly`, `SameSite=Lax`, `Secure` over TLS.
- [ ] Passwords stored as bcrypt hashes. Bootstrap admin password
      written to file (mode 0600), not logged.
- [ ] Access PIN is bcrypt-hashed at rest. Server-side rate limit
      keyed on `system_uuid` (5 / 15min).
- [ ] BitLocker PINs and recovery keys are AES-256-GCM encrypted at
      rest. Recovery-key history is append-only.
- [ ] AD service account password loaded from env, never logged.
- [ ] `scripts/check-secrets.sh` runs in CI and trips on common
      cleartext-secret patterns in `slog.String` / `fmt.Sprintf` calls.
- [ ] `Secret.Reveal()` calls are confined to the audited
      `internal/secrets` and `internal/auth` boundaries.
- [ ] Boot Client fails safe on every error path — imaging is never
      the default outcome of a failure.
- [ ] Bulk script action requires an authenticated session AND every
      operation row records the operator + target selection.
- [ ] Driver-package filters reject unknown keys at save time so a
      typo cannot silently never-match.

## Performance baseline (run before each release)

- N concurrent Boot Clients fetching the same WIM: throughput should
  scale linearly until disk read bandwidth is hit; the server is
  thin and CPU-bound only on TLS termination.
- Concurrent agents reporting after a bulk push: server should
  ingest at >= 100 reports/sec on commodity hardware with the
  default SQLite WAL mode.
- Bulk job claim latency: < 50ms p99 with 10k queued jobs.

These are sanity baselines; actual numbers depend on the host and
storage. The `metrics.txt` reference in `scripts/` (TODO) shapes the
load generator.

## What is intentionally NOT in this release

(See the design's non-goals and the project context primer.)

- Block-level disk cloning.
- macOS or Linux target imaging.
- Software-authoring tooling (deploy yes, build no).
- Per-image or multi-tenant branding.
- Merging of unattend objects up the chain.
- TFTP or layer-2 PXE.
- WMIC or WinPE in the boot environment.
- Storage of frozen historical images for routine re-imaging.
- Graded portal permissions.
