# Detection rules — reference

A **detection rule** answers the question "is this software
package already installed on this target?" When the agent
deploys a package, it evaluates the rules first; if every rule
reports "present", the package is skipped.

## The semantics

- A package can have **zero or more** rules.
- Every rule must report present (AND, not OR).
- Zero rules = re-install on every deployment (and the portal
  shows a hint warning you of that).
- Rules are evaluated in the order shown in the portal; the
  agent short-circuits on the first one that reports "not
  present".

## The four rule types

| Type | When to use it |
|---|---|
| **file** | The most common. "If this file exists at this path, the package is installed." Optional version + SHA-256 to pin to a specific build. |
| **registry** | When the installer registers itself but doesn't put a file at a known path. Most Windows installers write an `Uninstall` registry key. |
| **msi** | When the package is an MSI — Windows tracks MSI product codes and you can ask whether one is installed. |
| **script** | Fallback for cases the other three can't express. Run cmd/PowerShell; exit 0 = present, non-zero = not present. |

## File detection

| Field | Required? | What it is |
|---|---|---|
| File path | yes | Absolute Windows path. **Windows env vars are expanded**: `%ProgramFiles%`, `%ProgramFiles(x86)%`, `%LOCALAPPDATA%`, `%APPDATA%`, `%SystemRoot%`, `%TEMP%`, anything else exported in the agent's environment. |
| File version | optional | Exact-match the file's version metadata. Useful for pinning to a specific build. |
| File SHA-256 | optional | Hex-encoded SHA-256 of the file's bytes. Strictest possible match. |

If only **File path** is set, the rule reports present whenever
the file exists. If File version or SHA-256 are set too, all of
them must match.

### Examples

```
File path:    %ProgramFiles%\Microsoft VS Code\Code.exe
```

Present whenever VS Code's system installer has been run. Works
on both x64 and x86 hosts because `%ProgramFiles%` resolves to
the right place on each.

```
File path:    %ProgramFiles%\Acme\acme.exe
File version: 4.2.0.1138
```

Present only when Acme is installed AND its version is exactly
that build. Useful when you want a re-deploy to force an upgrade
from an older version.

```
File path:    %LOCALAPPDATA%\Programs\Slack\slack.exe
```

Per-user Slack install. Note the agent runs as SYSTEM by
default so `%LOCALAPPDATA%` resolves against SYSTEM's profile —
which is almost never what you want for per-user apps. Use a
script rule instead for per-user detection.

## Registry detection

| Field | Required? | What it is |
|---|---|---|
| Hive | yes | `HKLM` (most installers) or `HKCU` (per-user). |
| Key | yes | Path without the hive: `SOFTWARE\Acme`. |
| Value name | optional | A specific named value inside the key. |
| Value equals | optional | Exact-match a string value. |

Behavior:

- **Hive + Key only**: present whenever the key exists.
- **+ Value name**: present whenever the named value exists.
- **+ Value equals**: present whenever the named value's data
  equals the given string.

### Examples

```
Hive:        HKLM
Key:         SOFTWARE\Microsoft\Office\ClickToRun\Configuration
Value name:  ProductReleaseIds
```

Present whenever Office Click-to-Run has registered a product
release ID. Good "Office is installed at all" probe.

```
Hive:        HKLM
Key:         SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{EA457B21-F73E-494C-ACAB-524FDE069978}_is1
Value name:  DisplayVersion
Value equals: 1.94.0
```

Present only when VS Code 1.94.0 specifically is installed.

### The 32-bit gotcha

On 64-bit Windows, 32-bit installers write to
`HKLM\SOFTWARE\WOW6432Node\...` instead of `HKLM\SOFTWARE\...`.
If you can't see the key you expect, check WOW6432Node.

## MSI product code

| Field | Required? | What it is |
|---|---|---|
| MSI product code | yes | The GUID with braces: `{12345678-1234-1234-1234-123456789012}`. |

The agent queries Windows for that product code; present if
Windows says it's installed.

### Where to find the product code

- From an installed MSI: open the .msi in Orca or 7-Zip and look
  at the `Property` table → `ProductCode`.
- For a vendor MSI you have on disk:
  ```powershell
  Get-Package -Provider msi | Where-Object Name -like 'Acme*' | Select-Object Name,@{n='Code';e={$_.Metadata['ProductCode']}}
  ```

## Script detection

| Field | Required? | What it is |
|---|---|---|
| Shell | yes | `cmd` or `powershell`. |
| Body | yes | The script to run. |

Exit 0 = present. Anything else = not present. The body runs
non-interactively on the target as the agent's user (SYSTEM by
default).

### Examples

```
Shell:  powershell
Body:   if (Get-Service "AcmeAgent" -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }
```

Present whenever the AcmeAgent Windows Service exists.

```
Shell:  cmd
Body:   if exist "C:\ProgramData\Acme\installed.marker" (exit 0) else (exit 1)
```

Marker-file check.

## Picking the right type

| Symptom | Use |
|---|---|
| App has a stable install path with a known executable | **file** |
| App writes an Uninstall registry key under HKLM | **registry** |
| App is an MSI and you know its product code | **msi** |
| App is a portable ZIP extracted to a custom location | **file** (point at the extracted exe) |
| Per-user install (Slack, Teams personal, Spotify) | **script** with PowerShell that checks all user profiles |
| Conditional logic ("if X or Y is installed") | **script** — rule types AND together; for OR, encode it in one script |
| Detection by environment (joined-to-AD, license activated, etc.) | **script** |

## How the agent runs detection

1. Downloads the package's payload files (so SHA-256 checks have
   something to hash).
2. Iterates the rules in order.
3. For each rule:
   - file: `os.Stat(path)` after env-var expansion. Reads file
     version metadata only if needed.
   - registry: shells out to `reg query`.
   - msi: shells out to `reg query` against the Uninstall key.
   - script: spawns the shell with the body as input.
4. If any rule fails: short-circuit, run the install steps.
5. Otherwise: log `package.detected` and skip.

After install, re-runs detection to confirm the package is now
present. If still not present after install, logs
`package.install.unconfirmed` — your detection rule and your
install steps disagree about what "installed" means.

## See also

- [Tutorial 4 — Add software packages](tutorial-04-add-software.md) — worked examples.
- [Install steps reference](reference-install-steps.md) — what the agent runs when detection fails.
