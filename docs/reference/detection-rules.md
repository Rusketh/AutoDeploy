# Detection rules reference

A **detection rule** tells AutoDeploy how to decide whether a piece of
[software](../portal/software.md) is *already* installed on a machine. Before running a package's
[install steps](install-steps.md), the [agent](../introduction.md#agent-autodeploy-agent)
evaluates the package's detection rules; if they all pass, the package is considered present and
installation is skipped.

A package's detection rules are stored as a JSON **array**. A package counts as installed only when
**all** of its rules report present (a conservative AND). An empty array means "always reinstall".

```json
[
  { "type": "file", "file_path": "C:\\Program Files\\7-Zip\\7z.exe" }
]
```

## Rule types

Every rule has a `type` field — one of `file`, `registry`, `msi`, `script` — that selects which
other fields apply.

### `file`

Present if a file exists, optionally matching a version and/or SHA-256.

| Field | Required | Description |
|-------|----------|-------------|
| `file_path` | yes | Absolute path to the file to check. |
| `file_version` | no | Exact file version to match. |
| `file_sha256` | no | Hex SHA-256 digest to match. |

```json
{ "type": "file", "file_path": "C:\\Program Files\\Notepad++\\notepad++.exe" }
```

### `registry`

Present if a registry key exists, optionally checking a value name and comparing it.

| Field | Required | Description |
|-------|----------|-------------|
| `registry_hive` | yes | The hive, e.g. `HKLM`, `HKCU`. |
| `registry_key` | yes | Key path without the hive, e.g. `SOFTWARE\\Acme\\App`. |
| `registry_value` | no | Value name to read. |
| `registry_equals` | no | When set, the value must equal this. |

```json
{ "type": "registry", "registry_hive": "HKLM", "registry_key": "SOFTWARE\\Acme\\App", "registry_value": "Version", "registry_equals": "2.0" }
```

### `msi`

Present if an MSI product with the given product code is installed.

| Field | Required | Description |
|-------|----------|-------------|
| `msi_product_code` | yes | MSI product GUID, with braces, e.g. `{23170F69-40C1-2702-2301-000001000000}`. |

```json
{ "type": "msi", "msi_product_code": "{23170F69-40C1-2702-2301-000001000000}" }
```

### `script`

Runs a script; a non-zero exit code means "not detected". The body is written to a temporary
`.cmd` / `.ps1` file on the target and that file is run (so multi-line scripts and quoted paths
with spaces work, and PowerShell runs with its execution policy bypassed).

| Field | Required | Description |
|-------|----------|-------------|
| `script_shell` | yes | `cmd` or `powershell`. |
| `script_body` | yes | The script to run. |

```json
{ "type": "script", "script_shell": "powershell", "script_body": "if (Test-Path 'C:\\App') { exit 0 } else { exit 1 }" }
```

## Notes

- Backslashes in JSON strings must be escaped (`\\`), as in the examples above.
- AutoDeploy validates detection rules when you save a software package: an unknown `type` or a
  missing required field is rejected with a clear error (for example, a `file` rule without
  `file_path`).

See also: [install steps](install-steps.md) and the [Software portal guide](../portal/software.md).
</content>
