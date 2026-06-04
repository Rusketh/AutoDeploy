# Install steps reference

An **install step** is one action in a [software package's](../portal/software.md) install
sequence. When a package is not [detected](detection-rules.md) on a machine, the
[agent](../introduction.md#agent-autodeploy-agent) runs the package's steps **in order**.

Steps are stored as a JSON **array**; each element has a `type` field — one of `copy`, `unzip`,
`msi`, `appx`, `cmd`, `powershell`, `exe` — that selects which other fields apply.

```json
[
  { "type": "msi", "msi_path": "7z.msi", "msi_args": ["ALLUSERS=1"] }
]
```

## Fields common to every step

| Field | Required | Description |
|-------|----------|-------------|
| `type` | yes | The step type (see below). |
| `description` | no | A human-readable label for the step. |
| `success_codes` | no | Exit codes treated as success in addition to `0` (array of integers). |
| `continue_on_failure` | no | If `true`, a failure doesn't abort the rest of the package (default `false`). |

## Step types

### `copy`

Copy a file from one path to another.

| Field | Required | Description |
|-------|----------|-------------|
| `source_path` | yes | Source file. |
| `destination_path` | yes | Destination path. |

### `unzip`

Extract a `.zip` archive. Entries that would escape the destination are refused (zip-slip defence).

| Field | Required | Description |
|-------|----------|-------------|
| `source_path` | yes | The `.zip` file. |
| `destination_path` | yes | The extraction root. |

### `msi`

Run an MSI installer (always with `/quiet /norestart`).

| Field | Required | Description |
|-------|----------|-------------|
| `msi_path` | yes | The `.msi` file. |
| `msi_args` | no | Extra arguments appended after `/quiet /norestart`. |

```json
{ "type": "msi", "msi_path": "app.msi", "msi_args": ["INSTALLDIR=C:\\Acme"], "success_codes": [3010] }
```

### `appx`

Install an AppX / MSIX package.

| Field | Required | Description |
|-------|----------|-------------|
| `appx_path` | yes | The `.appx` / `.msix` file. |

### `cmd`, `powershell`

Run a script. The body is written to a temporary `.cmd` / `.ps1` file on the target and that file is
executed (`cmd /C <file>` or `powershell -ExecutionPolicy Bypass -File <file>`), so **multi-line
scripts work** and PowerShell runs even where the machine's execution policy would otherwise block an
unsigned script. The temporary file is deleted once the script has run.

| Field | Required | Description |
|-------|----------|-------------|
| `script_body` | yes | The script to run (interpreted by `cmd` or `powershell`). |

```json
{ "type": "powershell", "script_body": "Set-Service -Name W32Time -StartupType Automatic" }
```

### `exe`

Run an executable installer.

| Field | Required | Description |
|-------|----------|-------------|
| `exe_path` | yes | The `.exe` file. |
| `exe_args` | no | Command-line arguments (array of strings). |

```json
{ "type": "exe", "exe_path": "setup.exe", "exe_args": ["/S"] }
```

## Notes

- Steps run in array order. By default the first failing step (whose exit code is not `0` and not
  listed in `success_codes`) aborts the package; set `continue_on_failure` to override per step.
- **Path resolution** (applied to `source_path`, `destination_path`, `msi_path`, `appx_path`,
  `exe_path` and the arg lists): a bare filename resolves to an uploaded file or a file from an
  extracted [package bundle](../portal/software.md); `%pkgdir%` expands to the package work
  directory in **every** field of **every** step type (paths, args, and `cmd`/`powershell` script
  bodies); other Windows environment variables (`%ProgramData%`, `%ProgramFiles%`, …) are expanded
  too — **including on copy/unzip destinations**; absolute paths are used as-is.
- **Working directory.** For a multi-file package, install steps run **from the package work
  directory** (where the uploaded/extracted files live), so an installer's relative argument
  resolves there — e.g. an `exe` step `OfficeSetup.exe` with args `/configure NoTeams.xml` finds
  `NoTeams.xml` next to it. (It does *not* run from `C:\Windows\System32`, which is why a bare
  relative path used to be lost.)
- `copy` and `unzip` **create the destination directory** if it doesn't exist.
- Windows paths must be escaped in JSON (`\\`).
- AutoDeploy validates steps when you save a software package: an unknown `type` or a missing
  required field (for example a `copy` without `destination_path`) is rejected with a clear error.

See also: [detection rules](detection-rules.md) and the [Software portal guide](../portal/software.md).
</content>
