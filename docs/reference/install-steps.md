# Install steps reference

An **install step** is one action in a [software package's](../portal/software.md) install
sequence. When a package is not [detected](detection-rules.md) on a machine, the
[agent](../introduction.md#agent-autodeploy-agent) runs the package's steps **in order**.

Steps are stored as a JSON **array**; each element has a `type` field — one of `copy`, `unzip`,
`msi`, `appx`, `cmd`, `powershell`, `exe`, `winget` — that selects which other fields apply.

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
| `filter_os` | no | Run the step only on a matching OS — see [OS filters](#os-filters). |
| `success_codes` | no | Exit codes treated as success in addition to `0` (array of integers). |
| `continue_on_failure` | no | If `true`, a failure doesn't abort the rest of the package (default `false`). |

## OS filters

A step's optional `filter_os` gates it by operating system. When set, the agent runs the step only
on a target whose **OS name contains `filter_os`** as a **case-insensitive substring**; on any other
OS the step is **skipped** — passed over without running, which (unlike a failure) does **not** abort
the package. When `filter_os` is empty or absent (the common case) the step runs on every OS.

The OS name compared against is the machine's `Win32_OperatingSystem.Caption` — the same value shown
in the portal inventory, e.g. `Microsoft Windows 11 Pro` or `Microsoft Windows 10 Pro` — so
`"Windows 11"` matches only Windows 11, `"Windows 10"` only Windows 10, and `"Server"` matches the
Server editions.

This lets a single package carry steps for several editions and apply only the matching ones. For
example, ship a Windows 11 zip and a Windows 10 zip and extract whichever fits the target:

```json
[
  { "type": "unzip", "filter_os": "Windows 11", "source_path": "app-win11.zip", "destination_path": "C:\\Program Files\\App" },
  { "type": "unzip", "filter_os": "Windows 10", "source_path": "app-win10.zip", "destination_path": "C:\\Program Files\\App" }
]
```

On a Windows 11 machine the first step extracts and the second is skipped; on Windows 10 it's the
other way round. The deploy log records each skipped step (with its `filter_os`) so it's clear *why*
a step didn't run.

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

Run an MSI installer. The agent invokes `msiexec /i <msi_path> /quiet /norestart`, with any
`msi_args` appended after `/norestart`.

| Field | Required | Description |
|-------|----------|-------------|
| `msi_path` | yes | The `.msi` file. |
| `msi_args` | no | Extra arguments appended after `/i <msi_path> /quiet /norestart` (e.g. `TRANSFORMS=…`, public properties). |

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

### `winget`

Install a package with the Windows Package Manager. The agent runs
`winget install --id <winget_id> --silent --accept-package-agreements --accept-source-agreements
--disable-interactivity`, with any `winget_args` appended after `--silent`. Requires `winget` to be
available on the target.

| Field | Required | Description |
|-------|----------|-------------|
| `winget_id` | yes | The exact winget package identifier, e.g. `Microsoft.VisualStudioCode`. |
| `winget_args` | no | Extra arguments appended after `--silent` (array of strings, e.g. `["--scope", "machine"]`). |

```json
{ "type": "winget", "winget_id": "Microsoft.VisualStudioCode", "winget_args": ["--scope", "machine"] }
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
