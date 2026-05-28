# API quick-start

> Everything the portal does is available as JSON at `/api/v1/`.
> Use for CI tooling, scripts, configuration management.

## Authentication

The API uses the **same session cookie** as the portal.

```sh
# Login -> stores autodeploy_session in cookie.txt
PW=$(grep ^password ./data/admin-bootstrap.txt | sed 's/^password: //')
curl -c cookie.txt -X POST http://127.0.0.1:8080/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$PW\"}"

# All subsequent calls
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines
```

Open endpoints (no auth needed):

- `GET /api/v1/branding`
- `POST /api/v1/clients/menu` (Boot Client identity is the
  "authentication")
- `POST /api/v1/clients/validate-pin`
- `POST /api/v1/logs/ingest` (Boot Client / agent)
- `POST /api/v1/agent/*` (machine identity, with deploy token for
  secret-returning calls)
- `GET /healthz`, `GET /metrics`

## Standard CRUD shape

Each artifact follows the same pattern:

```
GET    /api/v1/{resource}        # list
POST   /api/v1/{resource}        # create
GET    /api/v1/{resource}/{id}   # read one
PUT    /api/v1/{resource}/{id}   # replace
DELETE /api/v1/{resource}/{id}   # delete
```

`{resource}` is one of `isos`, `unattends`, `drivers`, `software`,
`loadouts`, `images`, `machines`, `mirrors`.

## Status codes

| Code | Meaning |
|---|---|
| 200 | OK — payload returned. |
| 201 | Created. |
| 204 | No content (after `DELETE`). |
| 400 | Validation error (missing required field, bad JSON, etc.). |
| 401 | Unauthorised (missing or invalid session). |
| 404 | Resource not found. |
| 409 | Conflict — duplicate name, or object is referenced by others. |
| 422 | Inheritance cycle (image / loadout parent change). |
| 429 | Rate limited (log ingest, access-PIN). |
| 500 | Unexpected server error. |

## Recipes

### Build a full image catalog

```sh
# ISO
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/isos \
    -H 'Content-Type: application/json' \
    -d '{"name":"Win11","os_type":"windows-11","description":"Lab build"}'
curl -b cookie.txt -X PUT --upload-file ./Win11_24H2.iso \
    http://127.0.0.1:8080/api/v1/isos/1/upload
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/isos/1/extract

# Unattend (settings_json is what the structured editor stores)
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/unattends \
    -H 'Content-Type: application/json' \
    -d '{"name":"Win11 Office Standard",
         "settings_json":"{\"target_os\":\"windows-11\",\"locale\":\"en-GB\",...}"}'

# Driver pack
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/drivers \
    -H 'Content-Type: application/json' \
    -d '{"name":"Dell-Latitude-5520",
         "filters":[
           {"filter_json":"{\"system_manufacturer\":\"Dell Inc.\",\"system_product\":\"Latitude 5520\"}"}
         ]}'
curl -b cookie.txt -X PUT --upload-file ./dell-l5520.zip \
    http://127.0.0.1:8080/api/v1/drivers/1/upload
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/drivers/1/extract

# Software (detection + steps as JSON-in-JSON)
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/software \
    -H 'Content-Type: application/json' \
    -d '{"name":"7-Zip",
         "detection_json":"[{\"type\":\"registry\",\"registry_hive\":\"HKLM\",\"registry_key\":\"SOFTWARE\\\\7-Zip\"}]",
         "steps_json":"[{\"type\":\"msi\",\"msi_path\":\"{payload}\",\"success_codes\":[0,3010]}]"}'
curl -b cookie.txt -X PUT --upload-file ./7z2407-x64.msi \
    http://127.0.0.1:8080/api/v1/software/1/upload

# Loadout linking the software
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/loadouts \
    -H 'Content-Type: application/json' \
    -d '{"name":"Standard Apps",
         "packages":[{"package_id":1,"order_value":10}]}'

# Image composing the lot
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/images \
    -H 'Content-Type: application/json' \
    -d '{"name":"Office Workstation",
         "iso_id":1,"unattend_id":1,"loadout_id":1}'

# What the resolver hands the Boot Client for this image
curl -b cookie.txt http://127.0.0.1:8080/api/v1/images/1/resolved
```

### Inheritance + resolved view

```sh
# Parent
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/images \
    -d '{"name":"root","iso_id":1,"unattend_id":1}'

# Child inherits — nothing of its own
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/images \
    -d '{"name":"child","parent_id":1}'

# Resolved view shows the chain and the nearest-wins values
curl -b cookie.txt http://127.0.0.1:8080/api/v1/images/2/resolved
# {"image_id":2,
#  "chain_names":["child","root"],
#  "iso":{"name":"Win11",...},
#  "unattend":{"name":"default-ua",...},
#  "software":[...],
#  "diagnostics":[]}
```

### Bind a machine

```sh
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/machines/1/binding \
    -H 'Content-Type: application/json' \
    -d '{"image_id":1,
         "machine_name":"LAB-DL5520-001",
         "target_ou":"OU=Lab,DC=corp,DC=acme,DC=example",
         "group_memberships":["Lab-Computers","All-Workstations"]}'
```

### Set a BitLocker PIN

```sh
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/machines/1/bitlocker/pin \
    -H 'Content-Type: application/json' \
    -d '{"pin":"654321"}'

# Status (PIN value NOT returned)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines/1/bitlocker

# Retrieve the PIN (audited — emits secret.access log line)
curl -b cookie.txt http://127.0.0.1:8080/api/v1/machines/1/bitlocker/pin
```

### Queue a bulk operation

```sh
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/bulk/preview \
    -H 'Content-Type: application/json' \
    -d '{"name_regex":"^LAB-"}'

curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/bulk/operations \
    -H 'Content-Type: application/json' \
    -d '{"action":"script",
         "payload":"{\"shell\":\"powershell\",\"body\":\"Get-Service\"}",
         "target":{"name_regex":"^LAB-"}}'
```

### Search logs

```sh
curl -b cookie.txt \
    'http://127.0.0.1:8080/api/v1/logs?component=agent&since=2026-05-28T00:00:00Z&limit=200'
```

## Delete-with-reference guard

Deleting an ISO that an image still links returns 409:

```
{"error":"iso 1: in use (referenced by 1 images)"}
```

Resolve the reference first, then retry. The same guard applies
to unattends, software packages and loadouts.

## Per-subsystem references

| Subsystem | Doc |
|---|---|
| ISO upload / extract | [Payloads](payloads.md) |
| Unattend settings_json shape | [Unattend](unattend.md) |
| Driver filter rules + ingest | [Drivers](driver-matching.md) |
| Software detection + steps | [Software](software.md) |
| Loadouts | [Loadouts](loadouts.md) |
| Machine binding / history | [Inventory](inventory.md) |
| BitLocker | [BitLocker](bitlocker.md) |
| Bulk operations | [Bulk operations](bulk-operations.md) |
| Branding | [Branding](branding.md) |
| Logs | [Centralised logging](logging.md) |
