# Security

This page covers how AutoDeploy authenticates operators, protects data, and gates network
booting. For the values mentioned here, see the [configuration reference](../reference/configuration.md).

## Operator accounts and sessions

- Operators sign in with a **username and password**. Passwords are hashed with **bcrypt**;
  the plaintext is never stored.
- A successful login sets an **`autodeploy_session`** cookie (HttpOnly, SameSite=Lax), valid for
  **12 hours**. The `Secure` flag is set automatically when the server detects TLS — either a
  direct HTTPS connection or a reverse proxy that sets `X-Forwarded-Proto: https`.
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

Enable the HTTPS listener by setting `AUTODEPLOY_HTTPS_ADDR` (for example `0.0.0.0:443`) in
`/etc/default/autodeploy`. In production (`AUTODEPLOY_DEV=false`) you must also provide a
certificate and key via `AUTODEPLOY_TLS_CERT` and `AUTODEPLOY_TLS_KEY`.

If HTTPS is enabled without a cert/key **and** dev mode is on, the server generates a self-signed
certificate under `<data-dir>/tls/` and logs a warning. Use a CA-signed certificate for production.

The systemd unit grants only `CAP_NET_BIND_SERVICE` and runs the service as the unprivileged
`autodeploy` user inside a hardened sandbox (`ProtectSystem=strict`, `ProtectHome=true`,
`PrivateTmp=true`, with write access limited to the data directory).

## Secrets at rest

Sensitive values — notably [BitLocker](bitlocker.md) PINs and recovery keys — are encrypted at
rest. The encryption key comes from one of:

- `AUTODEPLOY_SECRETS_KEY` — a hex-encoded 32-byte key you supply, or
- an auto-generated key file `<data-dir>/secrets-key.bin` (created with `0600` permissions if the
  variable is unset).

Generate a key with:

```bash
openssl rand -hex 32
```

> **Back up the key off-host.** If you lose it, escrowed BitLocker PINs and recovery keys become
> unrecoverable. Include it (or the key file) in your [backup plan](backup-and-retention.md).

The repository includes `scripts/check-secrets.sh` to validate a secrets file's format.

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
