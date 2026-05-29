# Install steps — reference

An **install step** is one ordered action the agent runs on the
target when a software package needs installing.

## The semantics

- A package can have one or more steps.
- They run in the order shown in the portal.
- Each step has a list of **Success exit codes** (defaults to
  `[0]`).
- If a step exits with a code NOT in that list:
  - **Abort the whole package** (default): stops the package,
    moves on to the next package.
  - **Continue with the next step**: log the failure but keep
    going.

After all steps run, the agent re-evaluates the detection rules
to confirm the package is now "installed". A mismatch logs
`package.install.unconfirmed`.

## Path resolution — applies to every step's path fields

When a step references a path (Source path, MSI path, APPX path,
EXE path), the agent applies these rules:

| You write | Agent does |
|---|---|
| `setup.exe` (bare filename) | Resolves against the package's uploaded files. If found, swaps in the absolute path in the per-package work directory. If not found, used verbatim. |
| `C:\Program Files\Acme\foo.exe` (absolute) | Used verbatim. |
| `%ProgramFiles%\Acme\foo.exe` (env-var) | Expanded by the agent on the target. |
| `{payload}` (legacy single-file token) | Resolves to the uploaded file when there's exactly one. Ambiguous in multi-file packages. |

The **Destination path** in copy/unzip steps is NOT remapped —
that's where things go on the target, not where they come from.

## The seven step types

| Type | What it does |
|---|---|
| [Copy a file](#copy-a-file) | Copy from Source to Destination on the target. |
| [Extract a ZIP archive](#extract-a-zip-archive) | Unpack a .zip into a directory with zip-slip protection. |
| [Install an MSI](#install-an-msi) | `msiexec /i <path> /quiet /norestart [extra args]`. |
| [Install an APPX / MSIX](#install-an-appx--msix) | `Add-AppxPackage -Path <path>`. |
| [Run an EXE installer](#run-an-exe-installer) | Spawn the EXE with the given args. |
| [Run a cmd script](#run-a-cmd-script) | `cmd /C <body>`. |
| [Run a PowerShell script](#run-a-powershell-script) | Body piped to PowerShell via stdin. |

## Copy a file

| Field | Required? | What it is |
|---|---|---|
| Source path | yes | A file (bare filename, absolute, or env-var). |
| Destination path | yes | Where on the target the file should land. Absolute path. |

### Examples

```
Source:      branding.png
Destination: C:\ProgramData\OEM\Branding\logo.png
```

Copies an uploaded `branding.png` into a known location.

```
Source:      %SystemRoot%\System32\config\NTUSER.DAT
Destination: C:\ProgramData\Backups\NTUSER.bak
```

Copies an existing system file (note source isn't from the
package, it's from the target itself).

## Extract a ZIP archive

| Field | Required? | What it is |
|---|---|---|
| Source path | yes | The `.zip` file (bare filename of an uploaded zip, or an absolute path). |
| Destination path | yes | Directory the zip's contents should be extracted into. Created if it doesn't exist. |

Each entry's path is verified to live under the destination
**before any bytes are written** — zip-slip attacks (entries
with `..` segments or absolute paths) abort the extraction
without writing.

### Example

```
Source:      app-bundle.zip
Destination: C:\Tools\Acme
```

After extraction, files originally at `bin/foo.exe` inside the
zip end up at `C:\Tools\Acme\bin\foo.exe`. Later steps can
reference them by absolute path.

## Install an MSI

| Field | Required? | What it is |
|---|---|---|
| MSI file path | yes | The `.msi` (bare filename or absolute). |
| Extra msiexec args | optional | Appended after the default `/quiet /norestart`. |

The agent runs:

```
msiexec /i <path> /quiet /norestart <extra args>
```

### Examples

```
MSI path:   Acme.msi
```

Plain silent install.

```
MSI path:   Acme.msi
Extra args: TRANSFORMS=branding.mst INSTALLDIR=C:\Acme
```

With a transform and custom install location.

### Common MSI success codes to add

| Code | Meaning |
|---|---|
| `0` | Success (default; already in the success list). |
| `1641` | Initiated restart. Success, will reboot. |
| `3010` | Soft reboot required. Success. |

Add `3010,1641` to the Success exit codes field for any package
that asks for a reboot — otherwise the step is marked failed and
the package aborts.

## Install an APPX / MSIX

| Field | Required? | What it is |
|---|---|---|
| APPX / MSIX file path | yes | The `.appx`, `.msix`, or `.appxbundle` file. |

The agent runs:

```powershell
Add-AppxPackage -Path '<path>' -ErrorAction Stop
```

If the package is dependent on a runtime (VCRedist, .NET, etc.),
upload the runtime as a separate file in the same software
package and add an earlier APPX step for it.

## Run an EXE installer

| Field | Required? | What it is |
|---|---|---|
| EXE file path | yes | The `.exe`. |
| Args | optional | Space-separated; quoted args work as you'd expect. |

Spawns the EXE with the given args. The agent doesn't inject
silent flags — you supply them. Common ones:

| Installer convention | Silent flag |
|---|---|
| InnoSetup | `/VERYSILENT /MERGETASKS=!runcode` |
| NSIS | `/S` |
| Wise | `/s` |
| InstallShield (msiexec wrapper) | `/s /v"/qn /norestart"` |
| Squirrel (Atom, Slack-style) | `--silent` |
| Chocolatey wrapper | `/y` |
| Generic Windows installer | `/quiet` or `/silent` |

### Example

```
EXE path:   VSCodeSetup-x64-1.94.0.exe
Args:       /VERYSILENT /MERGETASKS=!runcode
```

## Run a cmd script

| Field | Required? | What it is |
|---|---|---|
| cmd /C script body | yes | The body. Multi-line is fine; use `&&` to chain. |

The agent runs:

```
cmd /C <body>
```

### Example

```
reg add HKLM\Software\Acme /v Installed /t REG_SZ /d 1 /f && ^
reg add HKLM\Software\Acme /v Version /t REG_SZ /d 4.2 /f
```

### When NOT to use this

cmd has limited quoting and limited syntax. For anything more
than a couple of registry edits or file moves, prefer
PowerShell.

## Run a PowerShell script

| Field | Required? | What it is |
|---|---|---|
| PowerShell script body | yes | The body. |

The agent pipes the body to:

```
powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command -
```

This means the body runs as one script block — multi-line is
fine, functions are fine, no execution-policy hassle.

### Reference uploaded files

The per-package work directory the agent picks for downloads is
available to the body. The easiest way to use it:

- For PowerShell-only packages that include helper files, use
  bare filenames in earlier copy/exe steps; PowerShell can
  reference them by absolute path after they've been copied.
- For a script that needs to find a sibling file in the package
  directly, use `$PSScriptRoot` — set by the agent to the work
  directory.

### Example

```powershell
$cfg = Get-Content "$PSScriptRoot\config.json" | ConvertFrom-Json
Set-ItemProperty -Path 'HKLM:\Software\Acme' -Name 'License' -Value $cfg.license
New-Item -Path 'C:\ProgramData\Acme' -ItemType Directory -Force | Out-Null
```

## Step ordering matters

The agent runs steps top-down. Common patterns:

**Stage files before the installer needs them**

```
Step 1: Copy config.json    → C:\Temp\Acme\config.json
Step 2: Run EXE acme.exe   /quiet /config="C:\Temp\Acme\config.json"
```

**Extract a bundle before referencing files inside**

```
Step 1: Extract app-bundle.zip → C:\Tools\Acme
Step 2: Run EXE C:\Tools\Acme\setup.exe /VERYSILENT
```

**Write a marker after a complex install (so script detection works)**

```
Step 1: Run PowerShell    (the actual install)
Step 2: Run cmd            "echo. > C:\ProgramData\Acme\installed.marker"
```

Then a script detection rule can check the marker.

## See also

- [Tutorial 4 — Add software packages](tutorial-04-add-software.md) — worked examples for VS Code and Office.
- [Detection rules reference](reference-detection-rules.md) — what the agent checks before running steps.
- [Troubleshooting](troubleshooting.md) — install-step symptoms and fixes.
