# Unattend configuration

> **Status.** Phase 5 + post-release catalog rebuild + expanded
> catalogue (local accounts, licensing, policies, commands).
> Structured settings model with curated dropdowns for locale,
> keyboard, time zone, edition, account groups, telemetry levels
> and power schemes; a target-OS picker that branches the generator
> between Windows 10/Server-style XML and Windows 11–specific XML;
> repeating tables for local accounts, specialize commands,
> SetupComplete.cmd lines and first-logon commands. Operators
> configure everything in a menu-driven form; the server emits
> the answer file Windows setup consumes.

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
| `kms_server`                   | *(omitted)*          | KMS host FQDN. Sets `slmgr.vbs /skms`.               |
| `kms_port`                     | `1688`               | KMS port. Only emitted when `kms_server` is set.     |
| `avma_key`                     | *(omitted)*          | Automatic Virtual Machine Activation key (Server VMs on a licensed Hyper-V host). Sets `slmgr.vbs /ipk`. |
| `skip_auto_activation`         | `false`              | Suppresses Windows' first-boot activation attempt.   |
| `local_accounts`               | `[]`                 | See below. Empty = no local accounts created.        |
| `auto_logon`                   | *(omitted)*          | See below.                                            |
| `admin_user` / `admin_password` | *(empty)*           | **Legacy.** Migrated to `local_accounts` on read; new unattends should use the table directly. |
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
| `telemetry_level`              | `0` (off)            | `0` leaves the OS default; `1`=Basic, `2`=Enhanced, `3`=Full. Sets the `AllowTelemetry` policy. |
| `disable_windows_update`       | `false`              | Sets the `NoAutoUpdate` policy.                       |
| `defer_feature_updates_days`   | `0` (off)            | Sets the `DeferFeatureUpdatesPeriodInDays` policy.    |
| `defer_quality_updates_days`   | `0` (off)            | Sets the `DeferQualityUpdatesPeriodInDays` policy.    |
| `enable_rdp`                   | `false`              | Enables Remote Desktop and opens the firewall.        |
| `allow_icmpv4`                 | `false`              | Adds an ICMPv4-echo inbound firewall rule.            |
| `power_scheme`                 | *(empty)* (Balanced) | `high` activates the High Performance scheme.         |
| `domain_join`                  | *(omitted)*          | See below.                                            |
| `specialize_commands`          | `[]`                 | See below.                                            |
| `setup_complete_commands`      | `[]`                 | See below.                                            |
| `first_logon_commands`         | `[]`                 | See below.                                            |

### Local accounts

Each entry creates one local account on the deployed machine:

```json
"local_accounts": [
  {"name": "Administrator", "password": "...", "group": "Administrators",
   "full_name": "Local Administrator", "description": "Lab admin"},
  {"name": "operator", "password": "...", "group": "Users",
   "full_name": "Lab Operator"}
]
```

- `group` is `Administrators` or `Users`.
- `password` is a **SECRET** — stored in the row, emitted into the
  XML (Windows needs it), never logged.
- The first `Administrators`-group account is *also* assigned to the
  built-in Administrator account via `<AdministratorPassword>`, so
  recovery tooling that looks at the built-in credential still finds
  a usable password.
- Operators upgrading from a row that used the old
  `admin_user`/`admin_password` fields don't need to re-enter
  anything: the loader migrates those into `local_accounts` on read.

### Auto-logon

Optional; logs the named account in automatically for N boots after
imaging completes (useful for post-imaging setup that needs a user
session):

```json
"auto_logon": {"username": "deployadmin", "password": "...", "count": 3}
```

The password must match the password of the named local account.

### Licensing (KMS / AVMA)

If `kms_server` is set the generator emits a `slmgr.vbs /skms`
specialize command pointed at the host:port. If `avma_key` is set
the generator emits a `slmgr.vbs /ipk` for the AVMA key. Set
`skip_auto_activation: true` to suppress Windows' first-boot
activation attempt when you intend to activate later.

### Windows policies

These translate to registry / netsh / powercfg commands run during
the specialize pass. They mirror the most common "every deployment
does this anyway" tweaks:

- `telemetry_level` writes the `AllowTelemetry` policy.
- `disable_windows_update` writes `NoAutoUpdate`.
- `defer_feature_updates_days` and `defer_quality_updates_days`
  write the deferral policies.
- `enable_rdp` flips `fDenyTSConnections` and opens the firewall
  group "remote desktop".
- `allow_icmpv4` adds an inbound ICMPv4-echo rule (so the box
  responds to ping).
- `power_scheme: "high"` activates the High Performance scheme via
  `powercfg /setactive`.

### Specialize commands

`RunSynchronousCommand` entries injected into the specialize pass —
run BEFORE OOBE pages render. Use for environment prep that has to
complete before the user sees anything:

```json
"specialize_commands": [
  {"order": 10, "description": "Lab tweak A",
   "command_line": "reg add HKLM\\Software\\Lab /v A /d 1 /f"}
]
```

Operator commands run AFTER the system-supplied policy / licensing /
RDP commands, so you can rely on the policy state when your command
fires.

### SetupComplete.cmd

Lines written into `%WINDIR%\Setup\Scripts\SetupComplete.cmd`.
Windows runs that file AFTER the specialize pass and BEFORE OOBE
finishes — a no-user-session window suited for system-level work
like enabling BitLocker pre-provisioning or scheduling a reboot:

```json
"setup_complete_commands": [
  {"order": 10, "description": "Pre-OOBE: enable BitLocker pre-provisioning",
   "command_line": "manage-bde -on C: -used"},
  {"order": 20, "description": "Pre-OOBE: schedule reboot",
   "command_line": "shutdown /r /t 0"}
]
```

Implementation detail: the generator base64-encodes the script and
emits a single PowerShell one-liner in the specialize pass that
writes the file. This handles arbitrary bytes safely and avoids the
quoting headaches of building the script line-by-line with `echo`.

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

Local-account passwords, the auto-logon password and the domain-join
password are secrets:

- They are stored in the unattend row's `settings_json` so the generator
  can include them in the XML (which Windows needs).
- They are **never** included in any log line, error message, or HTTP
  response body that is not the XML itself.
- `scripts/check-secrets.sh` enforces the no-leak rule statically.
- The generated XML is served only to authenticated clients once
  Phase 11 is in place. In Phase 5 the endpoint is open; treat dev
  environments accordingly.
