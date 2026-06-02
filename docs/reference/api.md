# JSON API reference

The AutoDeploy portal is a browser front-end over a JSON API rooted at **`/api/v1`**. Anything you
can do in the portal you can also automate over this API.

## Authentication

Operator endpoints require a session. Log in to obtain the `autodeploy_session` cookie, then send
it on subsequent requests:

```bash
# Log in (saves the session cookie to a jar)
curl -sc cookies.txt -X POST https://deploy.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<password>"}'

# Use the cookie on authenticated requests
curl -sb cookies.txt https://deploy.example.com/api/v1/isos
```

| Method & path | Purpose |
|---------------|---------|
| `POST /api/v1/auth/login` | Log in with `{ "username", "password" }`; sets the session cookie. |
| `POST /api/v1/auth/logout` | Invalidate the current session. |
| `GET /api/v1/auth/me` | Return the current operator. |

Resource endpoints follow REST conventions: `GET` (list), `POST` (create), `GET /{id}` (read),
`PUT /{id}` (update), `DELETE /{id}` (delete). Request bodies are JSON. IDs are integers.

### CSRF protection

All `POST`, `PUT` and `DELETE` requests to authenticated endpoints must include an
**`X-Requested-With`** header (any non-empty value). Requests without it receive `403 Forbidden`.
Add it to your scripts:

```bash
curl -sb cookies.txt -X POST https://deploy.example.com/api/v1/isos \
  -H 'Content-Type: application/json' \
  -H 'X-Requested-With: curl' \
  -d '{"name":"Win11-24H2","os_type":"windows11"}'
```

## Accounts

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/accounts` | List operator accounts. |
| `POST /api/v1/accounts` | Create an account: `{ "username", "password" }`. |
| `DELETE /api/v1/accounts/{id}` | Delete an account. |
| `POST /api/v1/accounts/{id}/disable` | Disable an account. |
| `POST /api/v1/accounts/{id}/enable` | Enable an account. |
| `POST /api/v1/accounts/{id}/password` | Change an account's password. |

## ISOs

`GET/POST /api/v1/isos`, `GET/PUT/DELETE /api/v1/isos/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `os_type` | yes | e.g. `windows11`. |
| `description` | no | |
| `storage_path`, `size_bytes` | no | Managed as the ISO is uploaded/extracted. |

Extraction/boot-media fields (`install_image_format`, `swm_parts`, `bootloader_present`,
`prep_error`, `media_*`, …) are server-managed and read-only.

## Unattends

`GET/POST /api/v1/unattends`, `GET/PUT/DELETE /api/v1/unattends/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `description` | no | |
| `settings_json` | no | A JSON document of Windows Setup settings (defaults to `{}`). |

## Drivers

`GET/POST /api/v1/drivers`, `GET/PUT/DELETE /api/v1/drivers/{id}`, plus
`POST /api/v1/drivers/{id}/preview` (preview which hardware a package matches).

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `description` | no | |
| `filters` | no | Array of `{ "filter_json": "<SMBIOS match JSON>" }`. Each `filter_json` is a JSON object of SMBIOS match keys. A key's value may be a single string or an array of strings (matched as OR), e.g. `{"system_product":["Latitude 5520","Latitude 5530"]}`. |
| `storage_path`, `size_bytes` | no | Managed as the package is uploaded. |

## Software

`GET/POST /api/v1/software`, `GET/PUT/DELETE /api/v1/software/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `description` | no | |
| `detection_json` | no | JSON array of [detection rules](detection-rules.md) (defaults to `[]`). |
| `steps_json` | no | JSON array of [install steps](install-steps.md) (defaults to `[]`). |
| `depends_on` | no | Array of software package IDs to install first. |
| `payload_filename`, `storage_path`, `size_bytes` | no | Managed as the payload is uploaded. |

## Loadouts

`GET/POST /api/v1/loadouts`, `GET/PUT/DELETE /api/v1/loadouts/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `description` | no | |
| `parent_id` | no | Inherit from another loadout. |
| `packages` | no | Array of `{ "package_id", "order_value", "opt_out" }`. |

## Images

`GET/POST /api/v1/images`, `GET/PUT/DELETE /api/v1/images/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `description` | no | |
| `parent_id` | no | Inherit from another image. |
| `iso_id` | no | ISO to deploy. |
| `unattend_id` | no | Unattend to apply. |
| `loadout_id` | no | Software loadout to apply. |
| `software_links` | no | Array of `{ "package_id", "order_value" }` for individually linked software. |

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/images/{id}/resolved` | The fully resolved image: the concrete media, drivers and software a deployment will apply. |

## Machines (inventory)

Machine records are created automatically when a machine network-boots or an agent checks in; they
are keyed by SMBIOS UUID. There is no create endpoint.

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/machines` | List machines. |
| `GET /api/v1/machines/{id}` | Machine detail. |
| `DELETE /api/v1/machines/{id}` | Remove a machine from inventory. |
| `GET /api/v1/machines/{id}/history` | Deployment history. |
| `GET /api/v1/machines/{id}/binding` | Current image binding. |
| `PUT /api/v1/machines/{id}/binding` | Set the binding: `{ "image_id", "machine_name", "target_ou", "group_memberships" }`. |
| `GET /api/v1/machines/{id}/detected` | Per-package detected state. |

### BitLocker

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/machines/{id}/bitlocker` | BitLocker status. |
| `GET/PUT /api/v1/machines/{id}/bitlocker/pin` | Get / set the BitLocker PIN. |
| `GET /api/v1/machines/{id}/bitlocker/recovery-keys` | List escrowed recovery keys. |
| `GET /api/v1/recovery-keys/{id}` | Retrieve a single recovery key. |

## Mirrors

`GET/POST /api/v1/mirrors`, `GET/PUT/DELETE /api/v1/mirrors/{id}`

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Unique. |
| `base_url` | yes | e.g. `https://mirror-eu.corp.example` (trailing slash is normalised away). |
| `description` | no | |
| `site` | no | Site tag; empty matches any site. |
| `priority` | no | Lower is preferred (defaults to 100). |

`healthy` and `last_checked` are server-managed.

## Bulk operations

| Method & path | Purpose |
|---------------|---------|
| `POST /api/v1/bulk/preview` | Preview which machines a target selection matches, without acting. Body: `{ "target": { … } }`. |
| `POST /api/v1/bulk/operations` | Create an operation and queue per-machine jobs. |
| `GET /api/v1/bulk/operations` | List operations (newest first). |
| `GET /api/v1/bulk/operations/{id}` | Operation detail with its jobs. |

**Create body:**

| Field | Required | Notes |
|-------|----------|-------|
| `action` | yes | One of `rename`, `software_push`, `script`, `reimage`. |
| `target` | yes | Selection: `{ "name_regex", "ou", "group", "machine_ids" }` (empty fields are ignored). |
| `payload` | yes | A JSON **string** carrying action-specific parameters (must be valid JSON). |
| `reimage_image_id` | for `reimage` | Image to deploy; `0`/omitted uses each machine's existing binding. |

## Branding

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/branding` | Get branding settings. |
| `PUT /api/v1/branding` | Update branding. |

Fields (all optional): `product_name` (defaults to `AutoDeploy`), `organisation_name`,
`support_url`, `support_phone`, `logo_data_url` (a `data:` URI for a small SVG/PNG),
`primary_color` (a CSS color), `oem_manufacturer` (written to the deployed machine's OEM
information).

## Access PIN

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/settings/access-pin` | Whether a PXE access PIN is set. |
| `PUT /api/v1/settings/access-pin` | Set the PIN: `{ "pin": "1234" }`. An empty string clears it. |

## Logs

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/logs` | Search the audit log (filter by component, actor, action, time range, …). |
| `POST /api/v1/logs/ingest` | Used by clients/agents to ship buffered log events. |

## Version & server update

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/version` | The server's build version (used by the Updates page). |
| `POST /api/v1/server/update` | Trigger an in-place server update. Requires the update helper and sudoers rule to be installed (see [Updates](../operations/updates.md)). Returns `503` if the helper is not available. |
| `GET /api/v1/server/update-log` | Retrieve the last 64 KB of the update log for diagnostics. Accepts an optional `?lines=N` query parameter to limit output. |

## Client & agent endpoints

The boot client and agent use a dedicated set of endpoints for AutoDeploy's internal protocol —
`POST /api/v1/clients/menu`, `POST /api/v1/clients/deploy-status`,
`POST /api/v1/clients/validate-pin`, and the `/api/v1/agent/*` family (check-in, hardware reports,
job results, software state, BitLocker config/escrow, self-update, and `domain-join`). These are
not intended for operator scripting and are listed here only for completeness.

`POST /api/v1/agent/domain-join` returns the [agent-driven domain join](../operations/active-directory.md#agent-driven-join-recommended)
configuration for the calling machine's bound image (domain, OU, join account and — only over this
authenticated agent channel — the join password). It is the runtime source of join credentials, so
they never need to be written into the unattend answer file.
</content>
