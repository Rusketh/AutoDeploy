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
| `AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` | `64` | Maximum concurrent payload (`/payload/*`) streams. See [Scaling](../operations/scaling.md). |
| `AUTODEPLOY_LOG_RETENTION_DAYS` | `0` | Delete audit-log entries older than this many days. `0` keeps everything. |

The Active Directory, payload-throttle and log-retention values seed portal settings on first boot
and can then be managed from **[Settings](../portal/settings.md)** without restarting the server.

### Validation

At startup the server enforces:

- **At least one listener** — `AUTODEPLOY_HTTP_ADDR` or `AUTODEPLOY_HTTPS_ADDR` must be set.
- **TLS in production** — when `AUTODEPLOY_HTTPS_ADDR` is set and `AUTODEPLOY_DEV` is `false`, both
  `AUTODEPLOY_TLS_CERT` and `AUTODEPLOY_TLS_KEY` must be provided.

## Data directory layout

Everything stateful lives under `AUTODEPLOY_DATA_DIR` (default `/var/lib/autodeploy` on a service
install):

| Path | Contents |
|------|----------|
| `autodeploy.sqlite` | The SQLite database (all records and settings). |
| `admin-bootstrap.txt` | One-time bootstrap admin password, written on first start. Delete after first login. |
| `secrets-key.bin` | Generated at-rest encryption key (when `AUTODEPLOY_SECRETS_KEY` is not set). |
| `tls/` | Auto-generated self-signed certificate (dev mode). |
| `ipxe/` | iPXE bootstrap binaries served over TFTP/HTTP for PXE boot. |
| `downloads/` | Agent and boot-client binaries handed out during deployment. |
| `isos/`, `unattends/`, `drivers/`, `software/`, `payloads/` | Extracted/uploaded payload blobs by category. |

Back up the entire data directory to protect both the database and the payloads — see
[Backup & retention](../operations/backup-and-retention.md).
</content>
