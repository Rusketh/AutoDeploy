# BitLocker management

> **Status.** Phase 12. BitLocker on C: with TPM+PIN, opt-in per machine
> via the inventory record. PINs and recovery keys are encrypted at rest
> with AES-256-GCM; the recovery-key history is append-only and is
> never overwritten.

## Opt-in via the inventory record

A machine is encrypted **only when** its inventory record has a PIN set.
**No PIN = no encryption.** This is a deliberate, meaningful state — not
a missing setting.

```sh
# Set a PIN (will be applied next time the agent runs on this machine)
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/machines/1/bitlocker/pin \
    -H 'Content-Type: application/json' \
    -d '{"pin":"654321"}'

# Inspect status (does NOT return the PIN value)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines/1/bitlocker
# {"machine_id":1,"pin_set":true,"updated_at":"..."}

# Retrieve the PIN (audited; never logged)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines/1/bitlocker/pin
# {"pin":"654321"}

# Clear the PIN (machine will not be re-encrypted on next deploy)
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/machines/1/bitlocker/pin \
    -d '{"pin":""}'
```

Every retrieval emits a `secret.access` log line capturing who retrieved
which secret and when — but never the value.

## What the agent does

On a fresh deploy, the agent:

1. Calls `POST /api/v1/agent/bitlocker/config` with its identity.
2. If the response has `pin_set: true`, it receives the cleartext PIN.
3. Calls `Enable-BitLocker -TpmAndPinProtector -EncryptionMethod Aes256
   -UsedSpaceOnly -SkipHardwareTest` via PowerShell, feeding the PIN
   over stdin (never on the command line).
4. Reads the generated recovery key and posts it to
   `POST /api/v1/agent/bitlocker/escrow`.
5. Logs `bitlocker.enabled` — the FACT only; the recovery key value
   never appears in any log.

If no PIN is configured the agent logs `bitlocker.skip` and moves on.
On non-Windows hosts (the dev agent) BitLocker is unsupported and the
agent logs `bitlocker.unsupported`.

## Recovery keys: append-only history

Every escrow appends a new row. **Old keys are never overwritten** so
keys for previous encryption states stay available to unlock historical
drives or images.

```sh
# History (does NOT include key values)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines/1/bitlocker/recovery-keys
# [{"id":42,"machine_id":1,"escrowed_at":"...","note":"deploy"}, ...]

# Retrieve a specific key (audited; never logged)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/recovery-keys/42
# {"id":42,"machine_id":1,"escrowed_at":"...","note":"deploy","key":"123456-789012-..."}
```

## Re-imaging

The PIN is preserved across re-image — the user's pre-boot
experience is unchanged. The recovery key, however, **must change** on
re-image: the volume is wiped, the old encryption keys cease to exist
and a new recovery key is generated. The new key is escrowed
automatically; the old one stays in the history forever.

## At-rest encryption

PINs and recovery keys are encrypted with AES-256-GCM before they hit
SQLite. The encryption key is sourced from:

- `AUTODEPLOY_SECRETS_KEY` — hex-encoded 32 bytes (preferred for
  production).
- Auto-generated to `$AUTODEPLOY_DATA_DIR/secrets-key.bin` (mode 0600)
  on first start when the env var is empty (dev mode convenience).

Losing the key means losing the ability to decrypt every escrowed PIN
and recovery key. **Back up the key separately** from the database.
