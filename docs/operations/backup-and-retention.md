# Backup & retention

Everything AutoDeploy needs to operate lives in its **data directory** (default
`/var/lib/autodeploy`). Backing that directory up — plus the secrets key — protects your entire
deployment.

## What to back up

The data directory contains:

- `autodeploy.sqlite` — the database (all records and settings),
- the payload blobs (`isos/`, `unattends/`, `drivers/`, `software/`, …),
- `secrets-key.bin` — the at-rest encryption key, **unless** you supply the key via
  `AUTODEPLOY_SECRETS_KEY` (in which case back up that value separately and securely).

See the [data directory layout](../reference/configuration.md#data-directory-layout) for the full
breakdown.

> Without the secrets key, escrowed [BitLocker](bitlocker.md) PINs and recovery keys in the
> database cannot be decrypted. Treat the key with the same care as the backup itself.

## Backing up

The repository ships `scripts/backup.sh`, which backs up the data directory (database and
payloads). Run it on a schedule (for example via `cron` or a systemd timer) and store the output
off-host.

A simple manual backup is also possible by archiving the data directory while the service is
stopped (or using a filesystem snapshot for a consistent copy while it runs).

## Restoring

To restore, install the server as usual, stop the service, replace the data directory with your
backup (including the secrets key), and start the service again.

## Log retention

The audit log can grow over time. Set `AUTODEPLOY_LOG_RETENTION_DAYS` to automatically delete log
entries older than the given number of days; `0` (the default) keeps everything. This is
configurable under [Settings → Operational](../portal/settings.md#operational).

## Notification retention

In-portal notifications are pruned separately from the audit log. Set the retention period under
[Settings → Notifications](../portal/settings.md#notifications) (default: 30 days, `0` = keep
forever). Webhook delivery logs are automatically pruned after 30 days.

Both run on the same hourly scheduler tick as log retention.

## Related

- [Configuration reference](../reference/configuration.md)
- [Security](security.md)
- [Logs](../portal/logs.md)
- [Notifications](../portal/notifications.md)
</content>
