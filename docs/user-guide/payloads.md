# Payload uploads and delivery

> **Status.** Phase 2. Upload, extraction and HTTP(S) delivery work for ISOs,
> driver-package blobs and software-installer blobs. Driver matching
> (Phase 4), unattend generation (Phase 5) and software step execution
> (Phase 6) build on top of these primitives.

## ISOs

Two-step flow: **upload**, then **extract**.

```sh
# 1. Upload the .iso file to a previously created ISO row (id=1 here).
curl -X PUT --upload-file ./Win11_24H2.iso \
    http://127.0.0.1:8080/api/v1/isos/1/upload
# {"id":1,"storage_path":"iso/1/source.iso","size_bytes":...}

# 2. Extract its contents. The server walks the ISO9660 tree, writes
#    every file into data/iso/1/files/, and records the install.wim or
#    install.esd path on the ISO row so the resolver can hand it out.
curl -X POST http://127.0.0.1:8080/api/v1/isos/1/extract
# {"id":1,"bytes":4738998272,"wim_path":"sources/install.wim",
#  "storage_path":"iso/1/files/sources/install.wim"}
```

Files inside the extracted ISO are then individually downloadable:

```sh
curl -O http://127.0.0.1:8080/payload/iso/1/sources/install.wim
```

Downloads honour `Range:` headers, so a Boot Client can resume an interrupted
fetch over a flaky link without restarting from byte zero.

## Driver packages and software installers

These are stored as opaque blobs in Phase 2; their on-the-wire format is
defined by Phase 4 (driver ingest) and Phase 6 (software step execution).

```sh
# Driver package payload.
curl -X PUT --upload-file ./dell-latitude-5520.zip \
    http://127.0.0.1:8080/api/v1/drivers/3/upload

# Software installer payload.
curl -X PUT --upload-file ./acme-suite-2026.msi \
    http://127.0.0.1:8080/api/v1/software/12/upload
```

Both are then served at:

```
GET /payload/drivers/{id}
GET /payload/software/{id}
```

## Deployment manifest

The endpoint a Boot Client calls after the operator picks an image:

```
GET /api/v1/images/{id}/manifest
```

returns a flat list of payload URLs derived from the resolved configuration:

```json
{
  "image_id": 4,
  "base_url": "https://autodeploy.lab/",
  "items": [
    {"role":"iso-wim",  "url":".../payload/iso/1/sources/install.wim",
       "name":"Win11","os_type":"windows-11","size_bytes":...},
    {"role":"software", "url":".../payload/software/12"},
    {"role":"unattend", "url":".../payload/unattend/4","name":"default-ua"}
  ],
  "warnings": []
}
```

The Boot Client is a fact reporter and step executor; it never computes any
of this. If the resolved image is missing an ISO or an unattend, the
manifest still returns and surfaces the problem via `warnings`.

## HTTPS

Set `AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443` and either `AUTODEPLOY_TLS_CERT` /
`AUTODEPLOY_TLS_KEY`, or leave them unset in `AUTODEPLOY_DEV=true` mode to
have a self-signed cert generated under `AUTODEPLOY_DATA_DIR/tls/`. Both
HTTP and HTTPS listeners run side by side if both addresses are configured;
in production set only the HTTPS address (or bind cleartext HTTP to
loopback behind a TLS-terminating reverse proxy).
