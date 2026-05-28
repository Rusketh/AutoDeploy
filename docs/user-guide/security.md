# Security: portal authentication and access PIN

> **Status.** Phase 11. Local username/password accounts gate the portal
> and JSON API; an optional global access PIN gates the Boot Client menu.
> Per the design, there is **no graded permission model** — every
> authenticated user has full access. Accountability rests on the audit
> trail.

## First-time login (bootstrap)

On its first run, the server creates an `admin` account with a random
password. The password is written to:

```
$AUTODEPLOY_DATA_DIR/admin-bootstrap.txt   (mode 0600)
```

The path appears in the server log; the **password value never does**.
Read the file once, log in, change the password, then delete the file.

```sh
PW=$(grep ^password ./data/admin-bootstrap.txt | sed 's/^password: //')
curl -c cookie.txt -X POST http://127.0.0.1:8080/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$PW\"}"
```

The login response sets a session cookie (`autodeploy_session`, HttpOnly,
SameSite=Lax, Secure on HTTPS). Subsequent requests pass the cookie back
to authenticate.

## Account management

All accounts have full portal/API access. Use the audit log for
accountability.

```sh
# Create
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/accounts \
    -H 'Content-Type: application/json' \
    -d '{"username":"bob","password":"correct-horse-battery-staple"}'

# List
curl -b cookie.txt http://127.0.0.1:8080/api/v1/accounts

# Disable / enable
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/accounts/2/disable
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/accounts/2/enable

# Reset password
curl -b cookie.txt -X POST http://127.0.0.1:8080/api/v1/accounts/2/password \
    -H 'Content-Type: application/json' \
    -d '{"password":"new-password"}'

# Delete
curl -b cookie.txt -X DELETE http://127.0.0.1:8080/api/v1/accounts/2
```

Passwords are stored as bcrypt hashes (cost 10). The cleartext is never
written to disk after the create/reset call returns.

## The deployment access PIN

The access PIN is an **optional** single global system setting that gates
the Boot Client menu. When enabled, every machine that network-boots is
challenged before any menu is shown.

```sh
# Enable (any non-empty value enables; the PIN is bcrypt-hashed at rest)
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/settings/access-pin \
    -H 'Content-Type: application/json' \
    -d '{"pin":"1234"}'

# Check status
curl -b cookie.txt http://127.0.0.1:8080/api/v1/settings/access-pin

# Disable (empty PIN)
curl -b cookie.txt -X PUT http://127.0.0.1:8080/api/v1/settings/access-pin \
    -H 'Content-Type: application/json' \
    -d '{"pin":""}'
```

### Fail-safe behaviour

The Boot Client prompts for the PIN before the menu and submits each
attempt to `POST /api/v1/clients/validate-pin`. Three wrong attempts and
the client fails safe to a normal boot — no menu shown, no imaging.

### Server-side rate limit

The local three-attempt limit could be defeated by rebooting; the server
also rate-limits by `system_uuid`. After **5 failed attempts in 15
minutes** the server returns `429 Too Many Requests` for that machine
regardless of how many times it reboots, until the window passes.

The PIN value itself is never logged; only the fact (and outcome) of an
attempt is recorded against the machine.

## What the audit trail captures

| Event                          | Source             |
|--------------------------------|--------------------|
| Login / logout                 | HTTP request log   |
| Account create / disable / delete / password reset | HTTP request log |
| Access-PIN enable / disable    | HTTP request log   |
| Access-PIN attempt (success and failure)            | `pin_attempt` table + request log |
| All artifact and image CRUD    | HTTP request log   |
| Boot Client menu fetch, deploy, agent reports       | per-component logs |

No secret value (password, PIN, recovery key, AD bind password) appears
in any of these. Centralised collection and search across all of them
arrives in Phase 14.
