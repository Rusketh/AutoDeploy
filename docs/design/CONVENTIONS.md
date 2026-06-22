# AutoDeploy Coding and Operational Conventions

These conventions apply across all three components (`server`, `boot-client`,
`agent`). They exist to enforce the guiding principles in the design document
from the very first line of code, so they do not have to be retrofitted later.

---

## 1. Language and toolchain

- Go 1.22 or newer for all three components.
- Each component is its own Go module (`server/`, `boot-client/`, `agent/`)
  with its own `go.mod`.
- Pin the Go toolchain in each `go.mod` via the `toolchain` directive.
- External dependencies must be vendored only when there is a reason; in
  general, use `go.sum` checksums and dependabot for updates.
- Standard library first. Add a dependency only when the standard library does
  not have a workable answer. (`chi` for routing, `pgx` for Postgres,
  `htmx` and templates for the portal UI.)

## 2. Project layout

Standard Go layout:

```
<component>/
  go.mod
  go.sum
  Makefile
  cmd/<binary>/main.go          # thin entry point
  internal/<area>/...           # all real code; not importable by others
```

`internal/` keeps the code per-component-private and lets each component
evolve independently.

## 3. Logging

- Structured logging via `log/slog` writing JSON to stdout.
- Every record carries the four required fields:
  - `actor`   — who initiated the action (a portal user, a SMBIOS UUID).
  - `action`  — what was done.
  - `component` — emitting component (`server.api`, `boot.imager`, etc.).
  - `target`  — the object acted on (machine id, image id, file path).
  Plus an automatic `time` timestamp and a `level`.
- The shared logger lives in each component's `internal/logging/` and wraps
  `slog` so calls look like:
  ```go
  log.Event(ctx, "deployment.start",
      slog.String("actor", machineUUID),
      slog.String("target", imageID))
  ```
- A request-scoped logger is attached to `context.Context` so deep call sites
  emit consistent fields without threading them by hand.

## 4. Secrets — the absolute rule

The following values MUST NEVER appear in any log, error message, panic
trace, metric label, HTTP response body (except where they are the response),
or test fixture committed to the repository:

- The deployment access PIN (the front-end gate).
- Domain-join service-account passwords.
- Webhook signing secrets.
- Portal account passwords (always hashed; never stored or logged in cleartext).
- AD service account credentials.

Enforcement:

- A `secret.String` wrapper type lives in each component's `logging` package.
  Its `String()` and `MarshalJSON()` methods always return `"[REDACTED]"`.
  All secret values move through this type from the moment they leave the
  validating boundary until the moment they are used.
- Access to a secret is itself a logged event: log the fact and actor of the
  retrieval, never the value.
- CI runs a `forbidden-strings` check (`scripts/check-secrets.sh`) that fails
  the build if `fmt.Sprint`, `log.Print*`, or `slog.String` are called with
  variables whose names look like secrets (`pin`, `recoveryKey`, `password`).
  The check is deliberately strict; opt out only via the wrapper type.

## 5. HTTP

- HTTP server: `net/http` + `chi` for routing.
- HTTPS is mandatory in production; the server must refuse to bind cleartext
  to non-loopback in production mode.
- Large payloads (ISOs, WIM, drivers) are streamed with
  `io.Copy(w, src)`; never `ioutil.ReadAll`. Range requests are supported so
  the Boot Client can resume interrupted downloads.

## 6. Errors

- Wrap with `fmt.Errorf("context: %w", err)`.
- The HTTP layer maps domain errors to status codes in one place
  (`internal/httpx/errors.go`); handlers do not write status codes ad-hoc.

## 7. Tests

- Table-driven unit tests for resolution rules and step execution.
- Integration tests for the API live under `server/internal/api/...` and use
  a real Postgres via `testcontainers-go` in CI (and on dev machines that
  have Docker; skipped with a SKIP message otherwise).
- A failed test fails the build.

## 8. Builds and CI

- Each module has a `Makefile` with `build`, `test`, `lint`, `vet` targets.
- GitHub Actions runs all three on every push.
- Builds are reproducible: pinned Go toolchain, pinned dependencies.
- A red build blocks "done". No phase is complete until CI is green.

## 9. The fail-safe rule

When the Boot Client or agent encounters a state it cannot resolve (server
unreachable, server returns 5xx, access denied, hardware identity unreadable),
the safe behaviour is: do nothing destructive. The Boot Client boots the
machine from its existing OS. The agent reports the failure on next check-in
and waits. Imaging must never proceed as the default outcome of a failure.

## 10. Authority lives on the server

The Boot Client and the agent never make security or resolution decisions
themselves. They report facts and execute what the API returns. If you find
yourself adding access logic, PIN checks, image inheritance walking, driver
filter evaluation or software set computation to a client, move it back to
the server.
