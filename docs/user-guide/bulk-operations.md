# Bulk operations

> **Status.** Phase 13. The Deployment Client runs resident (silent
> service mode) and checks in for queued bulk jobs. The operator queues
> per-machine work via a single bulk-operation request against an
> AD-centric target selection; the server resolves the target set,
> queues one job per machine, and the agent picks them up on the next
> check-in.

## Targeting

A target is any combination of:

```json
{
  "name_regex": "^LAB-",
  "ou":         "OU=Lab,DC=corp,DC=example",
  "group":      "Lab-Computers"
}
```

Empty fields are ignored. Preview a selection before running:

```sh
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/bulk/preview \
    -H 'Content-Type: application/json' \
    -d '{"name_regex":"^LAB-","ou":"OU=Lab,DC=corp,DC=example"}'
# [{...}, {...}]
```

## Actions

| `action`        | Payload shape                                                                  | Notes                                            |
|-----------------|--------------------------------------------------------------------------------|--------------------------------------------------|
| `rename`        | `{"new_name":"LAB-02"}`                                                        | Coordinates with the AD service (Phase 10) and reboots the machine. |
| `software_push` | a JSON array of `swspec.InstallStep` objects (or a single step object)         | Re-uses Phase 6 detection + step execution; idempotent. |
| `script`        | `{"shell":"cmd","body":"echo hi"}` or `{"shell":"powershell","body":"..."}`    | Highest-risk; every script run is audited.       |

## Creating an operation

```sh
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/bulk/operations \
    -H 'Content-Type: application/json' \
    -d '{
          "action":"script",
          "payload":"{\"shell\":\"powershell\",\"body\":\"Get-Service\"}",
          "target":{"ou":"OU=Lab,DC=corp,DC=example"}
        }'
```

The response includes the queued jobs (one per machine). Inspect later:

```sh
curl -b cookie.txt http://127.0.0.1:8080/api/v1/bulk/operations
curl -b cookie.txt http://127.0.0.1:8080/api/v1/bulk/operations/42
```

## What the resident agent does

When started with `--check-in 5m` (or similar), the agent loops:

1. POSTs identity to `/api/v1/agent/checkin`.
2. Server resolves the machine, claims up to 8 queued jobs (marking
   them `running`), returns them.
3. Agent executes each job by dispatching on `action`:
   - `script`: runs via `cmd /C` or `powershell -Command -` with the
     body on stdin.
   - `software_push`: runs the embedded install steps through the
     Phase 6 executor.
   - `rename`: calls `Rename-Computer -Force -Restart`.
4. Agent POSTs `{"status":"ok"|"failed","result_json":"..."}` to
   `/api/v1/agent/jobs/{id}/result`.
5. Sleeps `check-in` interval, repeats.

Offline machines pick up their queue when they next come online — the
queue persists.

## Safety

- **Bulk script** is the highest-risk action. Creating one requires an
  authenticated session; every operation row records the operator and
  the full target + payload. Combine with the audit trail to attribute
  any change made by a script.
- **Bulk rename** triggers a reboot. Make sure the target machines can
  reboot without losing in-progress work — pick a maintenance window.
- The check-in endpoint upserts the machine record on every call, so a
  machine that joins later still gets its queued jobs without operator
  intervention.
