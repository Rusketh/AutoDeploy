# Unattend configuration

> **Status.** Phase 5 + post-release catalog rebuild. Structured settings
> model with curated dropdowns for locale, keyboard, time zone and
> edition; a target-OS picker that branches the generator between
> Windows 10/Server-style XML and Windows 11–specific XML. Operators
> configure values in a menu-driven form; the server emits the answer
> file Windows setup consumes. Domain join is wired but is only used
> when AD integration is enabled on the configuration.

## Target OS

The form's first section asks you to pick a target OS:

| Pick                  | What the generator does differently                                 |
|-----------------------|---------------------------------------------------------------------|
| Windows 11            | Emits `BypassNRO` registry write in specialize (so Windows 11 22H2+ OOBE doesn't insist on an online Microsoft Account). Optionally emits the LabConfig `Bypass{TPM,SecureBoot,RAM,CPU,Storage}Check` keys when the "Bypass Windows 11 hardware requirements" toggle is on. |
| Windows 10            | Classic schema. The Windows 11 toggles are accepted but ignored.    |
| Windows Server 2019+  | Classic schema with server-edition catalog.                          |

Choosing a target OS also filters the **Edition** dropdown to the
correct list for that release.

## Catalogs

The form ships curated dropdowns so you don't have to memorise codes:

- **System locale / UI language**: ~50 common Windows locale codes with
  human labels (e.g. `en-US — English (United States)`).
- **Keyboard / input locale**: ~40 common KLID pairs (the cryptic
  `0409:00000409`-style codes are picked from the dropdown).
- **Time zone**: ~55 Windows time-zone IDs with UTC-offset labels
  (e.g. `(UTC-08:00) Pacific Time (US & Canada)`).
- **Edition**: branches on the chosen target OS to show only valid
  editions.

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
| `target_os`                    | `windows-11`         | Drives the generator. See above.                     |
| `bypass_nro`                   | `true`               | Windows 11 only: silences the OOBE online-account requirement.  |
| `bypass_win11_reqs`            | `false`              | Windows 11 only: sets LabConfig\Bypass*Check.        |
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
