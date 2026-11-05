# Security

This page covers how AutoDeploy authenticates operators, protects data, and gates network
booting. For the values mentioned here, see the [configuration reference](../reference/configuration.md).

## Operator accounts and sessions

- Operators sign in with a **username and password**. Passwords are hashed with **bcrypt**;
  the plaintext is never stored.
- A successful login sets an **`autodeploy_session`** cookie (HttpOnly, SameSite=Lax). The `Secure`
  flag is set automatically when the server detects TLS — either a direct HTTPS connection or a
  reverse proxy that sets `X-Forwarded-Proto: https`.
- Sessions **slide**: a session ends after **12 hours without activity**
  (`AUTODEPLOY_SESSION_IDLE_TIMEOUT`), and every page view or save pushes that back — but never past
  **7 days** from sign-in (`AUTODEPLOY_SESSION_MAX_LIFETIME`). About five minutes before an idle
  session ends the portal shows a bar with **Stay signed in**. When a session has ended, signing in
  again returns you to the page you were on (a form you were submitting is reopened, not re-posted),
  and background requests from portal pages get `401` instead of a sign-in page. See
  [Configuration](../reference/configuration.md).
- **Login rate limiting:** each IP address is limited to **10 login attempts per minute**. Further
  attempts receive `429 Too Many Requests` until the window expires.
- Manage accounts under **[Settings → Accounts](../portal/settings.md#accounts)**: create users,
  disable/enable them, delete them, and change passwords.

### First-run bootstrap

On first start, when no accounts exist, AutoDeploy creates an `admin` account and writes a
one-time password to **`<data-dir>/admin-bootstrap.txt`** (default
`/var/lib/autodeploy/admin-bootstrap.txt`). It also logs a warning pointing to that file.

After your first login:

1. Change the admin password in **Settings → Accounts**.
2. Delete the bootstrap file:
   ```bash
   sudo rm /var/lib/autodeploy/admin-bootstrap.txt
   ```

## Transport security (HTTPS)

Enable the HTTPS listener by setting an HTTPS address — either via the environment variable
`AUTODEPLOY_HTTPS_ADDR` (for example `0.0.0.0:443`) in `/etc/default/autodeploy`, or through
**Settings > [Network](../portal/settings.md#network)** in the portal. Provide a certificate and
key via the same settings page (path fields or file upload), or via the `AUTODEPLOY_TLS_CERT` and
`AUTODEPLOY_TLS_KEY` environment variables.

If HTTPS is enabled without a cert/key, the server generates a self-signed certificate under
`<data-dir>/tls/` and logs a warning. Use a CA-signed certificate for production.

### Reverse proxy

If you front the server with a reverse proxy (nginx, Caddy, Traefik, etc.), configure the
**Trusted proxy CIDRs** in **Settings > Network** so the server honours `X-Forwarded-For` for
client IP logging and `X-Forwarded-Proto` for secure-cookie detection. Only requests arriving
from the specified CIDRs will have their forwarded headers trusted.

For deployments where agents connect remotely but the portal should stay internal, set
**External access** to **Agent only**. This blocks the portal, login page, and admin API for any
request arriving through a trusted proxy — only agent endpoints (`/api/v1/agent/*`, `/payload/*`,
`/healthz`) are reachable externally. Operators must access the portal directly on the internal
network.

The systemd unit grants only `CAP_NET_BIND_SERVICE` and runs the service as the unprivileged
`autodeploy` user inside a hardened sandbox (`ProtectSystem=strict`, `ProtectHome=true`,
`PrivateTmp=true`, with write access limited to the data directory).

## Secrets at rest

Sensitive values — stored secrets such as domain-join passwords and webhook secrets — are
encrypted at rest. The encryption key comes from one of:

- `AUTODEPLOY_SECRETS_KEY` — a hex-encoded 32-byte key you supply, or
- an auto-generated key file `<data-dir>/secrets-key.bin` (created with `0600` permissions if the
  variable is unset).

Generate a key with:

```bash
openssl rand -hex 32
```

> **Back up the key off-host.** If you lose it, stored secrets such as domain-join passwords and
> webhook secrets become unrecoverable. Include it (or the key file) in your
> [backup plan](backup-and-retention.md).

### Tool kit Run As passwords

A [tool kit's](../portal/toolkits.md) Run As password, and any password an operator enters when
deploying a kit as a different account, are encrypted with the same key:

- **Never shown or carried in a page.** After saving, the portal only says a password is stored.
  The bulk review page carries an opaque token instead of the password; the token is held in
  server memory for at most 30 minutes and is tied to the operator who entered it.
- **Never put in a job payload.** The agent asks for the password just before the run, using its
  `agent_id`. The server releases it only for a job that belongs to that machine and is running.
  Every release is logged as `secret.access`, without the value.
- **Deleted when the operation ends.** A password entered at deploy time is deleted when the
  operation completes or is cancelled. The bulk scheduler also sweeps for and removes any left
  behind.
- On the machine, the agent uses the password once to log the account on and does not store it.

Software packages are restricted too. Their definitions can include licence keys, transforms or
response-file arguments. A package's definition and files are served only to an identified agent
whose machine needs that package, through its image or a running *Push software* job. Dependencies
are included. Nothing about a package is served anonymously.

Tool kit files are also restricted. They are served only to the agent of a machine that is running
the kit at that moment. On the machine, the agent's work folder (`C:\ProgramData\AutoDeploy\work`)
is limited to SYSTEM and Administrators. A Run As account gets read and execute rights on its own
job's folder, and nothing more.

AutoDeploy has no per-user permissions, so any portal user can run a kit as its stored account.
Give such accounts only the rights the kit needs.

The repository includes `scripts/check-secrets.sh`, a CI source-code tripwire (not a key
validator). It greps the Go source (`server/`, `boot-client/`, `agent/`) for secret-named values
(`pin`, `password`, `recoveryKey`, …) passed to `slog`/`fmt` logging or print calls, and for stray
`.Reveal()` usage, failing the build if a secret could leak into logs or HTTP responses.

## The PXE access PIN

Because a machine that network-boots can reach the deployment menu, you can require a numeric
**access PIN** before the boot menu will deploy. Set or clear it under
**[Settings → Access PIN](../portal/settings.md#access-pin)**. When set, the
[boot client](../introduction.md#boot-client-autodeploy-boot) must submit a valid PIN before it
can image a machine.

## API request protection

The JSON API enforces several safeguards:

- **CSRF protection:** all `POST`, `PUT` and `DELETE` requests to authenticated endpoints must
  include an `X-Requested-With` header (any non-empty value). Browsers cannot send custom headers
  cross-origin without a CORS preflight, which the server does not grant, so cross-site form
  submissions and simple fetches are blocked.
- **Request body limits:** JSON request bodies are capped at **10 MB**. Requests exceeding this
  are rejected before parsing.
- **Entity name validation:** names for ISOs, images, unattends, drivers, software and loadouts
  must be 1–200 characters and may not contain `<`, `>`, `"`, or null bytes.

## Audit logging

Operator and client actions are recorded in the [audit log](../portal/logs.md), searchable in the
portal. Use `AUTODEPLOY_LOG_RETENTION_DAYS` to prune old entries (see
[Backup & retention](backup-and-retention.md)).
</content>
