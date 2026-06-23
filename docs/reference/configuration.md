# Configuration reference

The AutoDeploy server is configured entirely through **environment variables**, read once at
startup. On a systemd install they live in `/etc/default/autodeploy`; the unit loads that file via
`EnvironmentFile`. After changing them, restart the service:

```bash
sudo systemctl restart autodeploy
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTODEPLOY_HTTP_ADDR` | `127.0.0.1:8080` | Address the HTTP portal/API listens on. Use `0.0.0.0:80` to serve on all interfaces. |
| `AUTODEPLOY_HTTPS_ADDR` | *(empty — disabled)* | Address for the HTTPS listener, e.g. `0.0.0.0:443`. |
| `AUTODEPLOY_TLS_CERT` | *(empty)* | Path to the PEM certificate. **Required** when HTTPS is enabled outside dev mode. |
| `AUTODEPLOY_TLS_KEY` | *(empty)* | Path to the PEM private key. **Required** when HTTPS is enabled outside dev mode. |
| `AUTODEPLOY_DATA_DIR` | `./data` | Root directory for the database, payloads, secrets and iPXE files. The installer sets this to `/var/lib/autodeploy`. |
| `AUTODEPLOY_DEV` | `false` | Dev mode. When `true`, a self-signed certificate is generated for HTTPS if no cert/key is given, and non-loopback HTTP is permitted. Leave `false` in production. |
| `AUTODEPLOY_TFTP_ADDR` | *(empty — disabled)* | Address for the built-in TFTP server, e.g. `:69`, for classic PXE. Leave empty if you already run a TFTP server. |
| `AUTODEPLOY_SECRETS_KEY` | *(empty)* | 64 hex characters (32 bytes) for at-rest encryption of secrets. If unset, a key file is generated under the data directory. See [Security](../operations/security.md). |
| `AUTODEPLOY_AD_URL` | *(empty)* | LDAP/LDAPS URL of your domain controller. Usually set in the portal instead. |
| `AUTODEPLOY_AD_BIND_DN` | *(empty)* | DN of the LDAP bind (service) account. |
| `AUTODEPLOY_AD_BIND_PASSWORD` | *(empty)* | Password for the bind account. |
| `AUTODEPLOY_AD_SEARCH_BASE` | *(empty)* | LDAP search base, e.g. `DC=example,DC=com`. |
| `AUTODEPLOY_AD_SKIP_TLS_VERIFY` | `false` | Skip LDAP TLS verification (testing only). |
| `AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` | `128` | Maximum concurrent payload (`/payload/*`) streams. Requests beyond this limit queue for up to 2 minutes before timing out. See [Scaling](../operations/scaling.md). |
| `AUTODEPLOY_LOG_RETENTION_DAYS` | `0` | Delete audit-log entries older than this many days. `0` keeps everything. |

The Active Directory, payload-throttle, log-retention, and network values seed portal settings on
first boot and can then be managed from **[Settings](../portal/settings.md)**. Network changes
(listen addresses, TLS paths) take effect on next restart; reverse proxy CIDRs and the external
URL take effect immediately.

### Startup behaviour

The server does not refuse to start over its listener configuration:

- **Listeners** — set `AUTODEPLOY_HTTP_ADDR` and/or `AUTODEPLOY_HTTPS_ADDR`. If neither is set the
  process still starts but serves nothing useful; `AUTODEPLOY_HTTP_ADDR` defaults to
  `127.0.0.1:8080`, so a listener is normally present unless you explicitly blank it.
- **HTTPS without a certificate** — when `AUTODEPLOY_HTTPS_ADDR` is set but no `AUTODEPLOY_TLS_CERT`
  / `AUTODEPLOY_TLS_KEY` is given, the server generates a self-signed certificate under
  `AUTODEPLOY_DATA_DIR/tls/` and starts anyway. In production (`AUTODEPLOY_DEV=false`) it logs a
  warning, because clients cannot verify a self-signed certificate — supply a real cert/key (or
  terminate TLS at a reverse proxy) for a trusted certificate. See [Security](../operations/security.md).

## Data directory layout

Everything stateful lives under `AUTODEPLOY_DATA_DIR` (default `/var/lib/autodeploy` on a service
install):

| Path | Contents |
|------|----------|
| `autodeploy.sqlite` | The SQLite database (all records and settings). |
| `admin-bootstrap.txt` | One-time bootstrap admin password, written on first start. Delete after first login. |
| `secrets-key.bin` | Generated at-rest encryption key (when `AUTODEPLOY_SECRETS_KEY` is not set). |
| `tls/` | TLS certificates — auto-generated self-signed cert or operator-uploaded PEM files via the portal. |
| `ipxe/` | iPXE bootstrap binaries and the Boot Client kernel/initrd, served over TFTP/HTTP for PXE boot. |
| `downloads/` | Agent and boot-client binaries handed out during deployment and from the portal Downloads page. |
| `iso/`, `drivers/`, `software/`, `updates/` | Extracted/uploaded payload blobs by category (ISO trees + source ISO, driver packages, software installers, Windows Update packages). |

Back up the entire data directory to protect both the database and the payloads — see
[Backup & retention](../operations/backup-and-retention.md).
</content>
