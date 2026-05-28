# Centralised logging

> **Status.** Phase 14. Per-component structured logging has been live
> from Phase 0; Phase 14 adds the central store, the ingest endpoint
> the Boot Client and agent use to ship events back, and the
> search-and-view API the portal renders.

## What is logged

Every event captures the four required fields plus a timestamp:

| Field        | Value                                                      |
|--------------|------------------------------------------------------------|
| `actor`      | Who initiated the action — portal user, machine UUID, "system". |
| `action`     | What was done (a dotted name like `package.install.ok`).   |
| `component`  | Which component produced the event (`server.api`, `boot.imager`, etc.). |
| `target`     | The object acted on (machine id, image id, file path).     |
| `occurred_at`| Timestamp (UTC).                                            |
| `level`      | INFO / WARN / ERROR.                                        |
| `fields`     | Extra structured attributes as JSON.                       |

## Ingest

Components ship events to:

```
POST /api/v1/logs/ingest
{ "events": [
    {"component":"boot","level":"INFO","actor":"<uuid>","action":"deploy.apply.start",
     "target":"/dev/sda","fields":"{\"dry_run\":false}"}
] }
```

The server appends each event in a transaction. A best-effort tripwire
rejects events whose `fields` payload contains a clear-text password,
PIN or recovery key — these are bugs to fix at the emitter, not values
to silently store.

## Searching

```sh
curl -b cookie.txt 'http://127.0.0.1:8080/api/v1/logs?component=boot&since=2026-05-28T00:00:00Z&limit=200'
```

Query parameters:

| Param        | Meaning                                                    |
|--------------|------------------------------------------------------------|
| `component`  | Exact match on emitting component.                         |
| `actor`      | Exact match on the actor field (a UUID or username).       |
| `action`     | Exact match on the action name.                            |
| `since`      | RFC3339 timestamp lower bound.                              |
| `until`      | RFC3339 timestamp upper bound.                              |
| `limit`      | Up to 10000, default 1000.                                 |

Results are returned newest first.

## Retention

The `log_event` table grows; an operator-side cron or scheduled job
should periodically call:

```sh
# Prune anything older than 90 days. (Server CLI hook lands in Phase 16.)
```

For now, retention is whatever you periodically `DELETE FROM
log_event WHERE occurred_at < ?`. A scheduled prune driver will be
added in Phase 16 alongside the rest of the operational housekeeping.

## Secrets, again

Detailed logging is the system-wide rule; **secrets are the absolute
exception**. PINs, recovery keys, passwords and AD bind credentials
**never** appear in any log row, in `fields`, or in any error or
HTTP response that ends up in `fields`. The fact and actor of a
secret retrieval do appear (as a `secret.access` event), so that
sensitive accesses are attributable.
