# Driver matching

> **Status.** Phase 4. Structured SMBIOS-attribute filters on driver
> packages; server-side matching against reported hardware; manifest
> includes matched driver URLs; portal-friendly preview endpoint.

Driver matching is **global**: a package's filters are evaluated against
the reported hardware regardless of which image the operator selected.
Image inheritance does not scope drivers. If a package's filter matches,
the package applies.

## Filter shape

A filter is a JSON object whose keys are SMBIOS attribute names and whose
values are the required values:

```json
{"system_manufacturer": "Dell Inc.", "system_product": "Latitude 5520"}
```

Allowed keys:

```
system_manufacturer  system_product  system_serial  system_uuid
bios_vendor          bios_version
board_manufacturer   board_product   board_serial
```

Match semantics:

- **A package** matches if **any** of its filters matches the hardware
  (logical OR across filters).
- **A filter** matches if **every one** of its key/value constraints
  matches the hardware (logical AND within a filter).
- String compares are **case-insensitive** with whitespace trimmed.
- A value of `"*"` (or empty string) matches any non-empty reported
  value for that attribute. Use sparingly.
- An **empty filter (no constraints) matches nothing.** This is
  intentional — a typo that left a filter blank would otherwise inject
  drivers into every machine.

## Defining a filter

```sh
curl -X POST http://127.0.0.1:8080/api/v1/drivers \
    -H 'Content-Type: application/json' \
    -d '{
          "name": "Dell-Latitude-5520",
          "description": "NIC, GPU, chipset for Latitude 5520",
          "filters": [
            {"filter_json": "{\"system_manufacturer\":\"Dell Inc.\",\"system_product\":\"Latitude 5520\"}"}
          ]
        }'
```

Unknown keys are rejected at save time so a typo cannot silently
never-match:

```
{"error":"validation failed: unknown filter key \"systme_manufacturer\" (allowed: [...])"}
```

## Previewing a filter against hypothetical hardware

```sh
curl -X POST http://127.0.0.1:8080/api/v1/drivers/1/preview \
    -H 'Content-Type: application/json' \
    -d '{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520"}'
```

Response:

```json
{
  "driver_id": 1,
  "name": "Dell-Latitude-5520",
  "identity": {"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520",...},
  "filters": [
    {"filter_id": 1, "filter_json": "...", "matches": true}
  ],
  "package_matches": true
}
```

`matches: false` on every filter and `package_matches: false` mean the
package would NOT be injected onto a machine with that identity.

## In the manifest

The Boot Client posts its identity together with the image id:

```
POST /api/v1/images/{id}/manifest
{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520", ...}
```

The response includes one `driver` item per matched package:

```json
{
  "items": [
    {"role":"iso-wim", "url":".../payload/iso/1/sources/install.wim", ...},
    {"role":"driver",  "url":".../payload/drivers/1", "name":"Dell-Latitude-5520", "size_bytes":...}
  ],
  "warnings": []
}
```

If no packages matched, a diagnostic appears in `warnings` (the deployment
still proceeds — drivers are additive).

## Pitfalls

- **Over-broad filters.** A filter like
  `{"system_manufacturer":"Dell Inc."}` matches every Dell machine; if the
  package contains drivers for one specific model, that is a problem.
  Make filters as specific as the driver requires; the portal preview
  endpoint exists for exactly this kind of sanity check.
- **No filters = never injected.** A driver package with an empty filter
  set never matches anything. Add at least one filter with at least one
  constraint.
- **Driver matching is global.** The driver list resolved for a given
  deploy depends on the hardware identity, not on which image the
  operator selected. Two images deploying to the same hardware get the
  same drivers.
