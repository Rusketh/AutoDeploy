# Inventory and bindings

> **Status.** Phase 8. Every machine that boots the AutoDeploy client is
> recorded; bindings link a machine to its assigned image, name, OU and
> AD groups; deployment history is dated and append-only. Re-imaging
> (Phase 9) and bulk operations (Phase 13) use the binding.

## The machine record

Keyed on the **SMBIOS UUID** (with serial as a secondary identifier),
each `machine_record` carries the firmware identity reported by the
Boot Client plus first-seen / last-seen timestamps.

A machine is created automatically the first time its Boot Client
reports identity to `/api/v1/clients/menu` — no operator step needed.

```sh
curl http://127.0.0.1:8080/api/v1/machines
```

## Bindings

A binding is what the machine is **assigned to**:

```json
{
  "machine_id": 1,
  "image_id": 7,
  "machine_name": "LAB-01",
  "target_ou": "OU=Lab,DC=corp,DC=example",
  "group_memberships": ["Lab-Computers", "All-Workstations"]
}
```

Set or update with `PUT /api/v1/machines/{id}/binding`. Once a binding
exists, the Boot Client menu offers a **Re-image** option for that
machine — rebuilding the binding's image against the **current**
definition (Phase 9).

If the bound image is deleted, the binding's `image_id` is set to NULL
rather than the binding being removed; the binding is the machine's
*assignment*, separate from the image that currently fulfils it.

## Deployment history

Append-only. The agent opens an `in_progress` row at the start of a
deploy and updates it to `ok` or `failed` at the end:

```sh
curl http://127.0.0.1:8080/api/v1/machines/1/history
```

Each row: `started_at`, `completed_at`, `image_id`, `outcome`, `notes`.

The design is explicit: this is an **audit log**, not a replay manifest.
Re-imaging always uses the **latest** definition of the bound
configuration, never a frozen historical snapshot.

## Detected state and drift

The agent reports per-package detection state in its final report:

```json
"packages": [
  {"package_id": 10, "detected": true,  "installed": true,  "failed": false},
  {"package_id": 11, "detected": false, "installed": false, "failed": true}
]
```

This is persisted as `machine_detected_state` and exposed at
`GET /api/v1/machines/{id}/detected` for drift reporting in the portal.
