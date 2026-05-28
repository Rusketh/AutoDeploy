# Unattend configuration

> **Status.** Phase 5. Structured settings model and complete
> `unattend.xml` generation. Operators configure values; the server emits
> the answer file Windows setup consumes. Domain join is wired but is
> only used when AD integration (Phase 10) is enabled on the
> configuration.

## Resolution

Unattend follows the design's **nearest-wins, used in full** rule. The
nearest linked unattend up the image's parent chain is used as-is; there is
no merging. Every unattend must be complete on its own. A "base" unattend
is a template you clone and customise — not a fragment that merges down.

If the chain has no linked unattend, the resolver returns a diagnostic and
the manifest carries no `unattend` item; the machine will boot Windows
setup interactively.

## Settings

The unattend row's `settings_json` is a JSON object with these fields. All
are optional; sensible defaults are filled in:

| Field                          | Default              | Notes                                                |
|--------------------------------|----------------------|------------------------------------------------------|
| `locale`                       | `en-US`              | Windows locale string.                               |
| `ui_language`                  | same as `locale`     |                                                      |
| `keyboard`                     | `0409:00000409`      | Input locale.                                        |
| `time_zone`                    | `GMT Standard Time`  | Windows time-zone name.                              |
| `edition`                      | `Windows 11 Pro`     |                                                      |
| `product_key`                  | *(omitted)*          | KMS clients omit; retail keys go here.               |
| `admin_user`                   | `Administrator`      | Local administrator account name.                    |
| `admin_password`               | *(empty)*            | **SECRET.** Stored in the row, included in the generated XML (Windows needs it), never logged. |
| `hide_admin`                   | `false`              | If `true`, skip OOBE local-account creation (use for AD-only configurations). |
| `name_strategy`                | `random`             | One of `random` (Windows default `WIN-…`), `literal`, `prefix`. |
| `computer_name`                | *(empty)*            | Used by `literal` and `prefix` strategies.           |
| `skip_machine_oobe`            | `true`               |                                                      |
| `skip_user_oobe`               | `true`               |                                                      |
| `hide_eula`                    | `true`               |                                                      |
| `hide_oem_registration`        | `true`               |                                                      |
| `hide_online_account_screens`  | `true`               |                                                      |
| `hide_wireless_setup`          | `true`               |                                                      |
| `protect_your_pc`              | `3`                  | `1` = express settings, `3` = skip.                  |
| `domain_join`                  | *(omitted)*          | See below.                                            |
| `first_logon_commands`         | `[]`                 | See below.                                            |

### Domain join

```json
"domain_join": {
  "domain": "corp.example",
  "ou": "OU=Lab,DC=corp,DC=example",
  "join_user": "joiner@corp.example",
  "join_password": "..."     // SECRET — never logged
}
```

The full server-side AD integration (delete-and-replace lifecycle, group
membership reconciliation) arrives in Phase 10; the unattend section above
is what Windows uses to join.

### First-logon commands

An ordered list. Each command runs synchronously on first boot:

```json
"first_logon_commands": [
  {"order": 10, "description": "Disable hibernation", "command_line": "powercfg /h off"},
  {"order": 20, "description": "Install corp root CA", "command_line": "certutil -addstore Root corp-root.crt"}
]
```

The generator always appends an additional first-logon command that starts
the AutoDeploy agent. Operator commands run before it. Order values are
sorted before emission; the agent command is given an order higher than
any operator command so it runs last.

## Generating the XML

The endpoint is per-image (because resolution depends on the image chain):

```
GET /payload/unattend/{image-id}
```

The response is the generated `unattend.xml` with `Content-Type:
application/xml`. The Boot Client downloads it via the same manifest item
it always downloads (`role: "unattend"`), and writes it to
`Windows\Panther\unattend.xml` inside the applied image.

## Secret handling

The local-administrator password and the domain-join password are
secrets:

- They are stored in the unattend row's `settings_json` so the generator
  can include them in the XML (which Windows needs).
- They are **never** included in any log line, error message, or HTTP
  response body that is not the XML itself.
- `scripts/check-secrets.sh` enforces the no-leak rule statically.
- The generated XML is served only to authenticated clients once
  Phase 11 is in place. In Phase 5 the endpoint is open; treat dev
  environments accordingly.
