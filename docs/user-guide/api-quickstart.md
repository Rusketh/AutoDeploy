# API quick-start

> **Status.** Phase 1 ships the JSON CRUD API for the standalone artifacts
> (ISO, Unattend, DriverPackage, SoftwarePackage) and the Image composition
> object, plus an endpoint that returns an image's resolved configuration.
> Authentication, payload uploads, the access PIN and the Boot Client menu
> arrive in later phases.

The API is mounted under `/api/v1/`. The dev server defaults to
`http://127.0.0.1:8080`.

## Resources

| Resource                  | URL prefix           |
|---------------------------|----------------------|
| ISOs                      | `/api/v1/isos`       |
| Unattends                 | `/api/v1/unattends`  |
| Driver packages           | `/api/v1/drivers`    |
| Software packages         | `/api/v1/software`   |
| Images                    | `/api/v1/images`     |
| Resolved image (read-only) | `/api/v1/images/{id}/resolved` |

Each resource supports the standard set of operations:

```
GET    /api/v1/{resource}        # list
POST   /api/v1/{resource}        # create
GET    /api/v1/{resource}/{id}   # read one
PUT    /api/v1/{resource}/{id}   # replace
DELETE /api/v1/{resource}/{id}   # delete
```

## Status codes

| Code | Meaning in this API                                                |
|------|--------------------------------------------------------------------|
| 200  | OK — payload returned.                                             |
| 201  | Created.                                                           |
| 204  | No content (after `DELETE`).                                       |
| 400  | Validation error (missing required field, bad JSON, etc.).         |
| 404  | Resource not found.                                                |
| 409  | Conflict — duplicate name, or object is referenced by others and cannot be deleted. |
| 422  | Inheritance cycle — proposed image parent change would create one. |
| 500  | Unexpected server error.                                           |

## Examples

### Create an ISO and an Unattend

```sh
curl -s -X POST http://127.0.0.1:8080/api/v1/isos \
    -H 'Content-Type: application/json' \
    -d '{"name":"Win11","os_type":"windows-11","description":"Lab build"}'

curl -s -X POST http://127.0.0.1:8080/api/v1/unattends \
    -H 'Content-Type: application/json' \
    -d '{"name":"default-ua","description":"Lab unattend"}'
```

### Create a parent image and a child that inherits from it

```sh
# Parent image links the ISO and unattend explicitly.
curl -s -X POST http://127.0.0.1:8080/api/v1/images \
    -H 'Content-Type: application/json' \
    -d '{"name":"root","iso_id":1,"unattend_id":1}'

# Child image links nothing — resolution will inherit from root.
curl -s -X POST http://127.0.0.1:8080/api/v1/images \
    -H 'Content-Type: application/json' \
    -d '{"name":"child","parent_id":1}'

# Inspect the resolved configuration for the child.
curl -s http://127.0.0.1:8080/api/v1/images/2/resolved
# {"image_id":2, "iso":{"name":"Win11",...}, "unattend":{"name":"default-ua",...},
#  "chain_names":["child","root"], ...}
```

### Driver packages with SMBIOS filters

```sh
curl -s -X POST http://127.0.0.1:8080/api/v1/drivers \
    -H 'Content-Type: application/json' \
    -d '{
          "name":"Dell-Latitude-5520",
          "filters":[
              {"filter_json":"{\"system_manufacturer\":\"Dell Inc.\",\"system_product\":\"Latitude 5520\"}"}
          ]
        }'
```

(Phase 4 ships the structured filter type and the actual matching engine; in
Phase 1 the filter is stored as opaque JSON.)

### Delete-with-reference guard

Deleting an ISO that an image still links returns 409:

```
{"error":"iso 1: in use (referenced by 1 images)"}
```

Resolve the reference first (delete or re-point the image), then retry.
