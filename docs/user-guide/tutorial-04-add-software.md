# Tutorial 4 — Add software packages

Add VS Code, Office, OEM utilities, custom apps — anything you'd
normally install by hand after Windows is up — so they're already
installed when the operator first logs in.

Prerequisite: [Tutorial 3](tutorial-03-first-deploy.md) — you can
deploy a vanilla Windows install.

## The mental model

A **software package** in AutoDeploy is three things:

1. **Files you upload** — the installer EXE, MSI, APPX, ZIP,
   helper scripts, configuration XML, anything the install
   needs. Multi-file is fine and common.
2. **Detection rules** — how the agent decides "is this already
   installed?" so a re-deploy doesn't reinstall what's already
   there.
3. **Install steps** — the ordered list of commands the agent
   runs on the target.

Then you link the package to an image. The agent, running on the
target after Windows comes up, downloads the files, evaluates the
detection rules, and (if not detected) runs the install steps in
order.

Two worked examples. The first is the simple case; the second
shows multi-file + bare-filename references.

## Example A — Single-file installer: Visual Studio Code

VS Code's system installer is a single `.exe`. We'll set it up
to install silently and detect with a file rule.

### A.1 — Create the package

1. **Software** in the nav → **+ New software package**.
2. **Name**: `Visual Studio Code`. **Description**: free text.
3. **Create**. You land on the edit page.

### A.2 — Upload the installer

Download Microsoft's system installer:
<https://code.visualstudio.com/download> → "System Installer".

In the portal's **Payload files** card:

1. Choose file → `VSCodeSetup-x64-1.94.0.exe`.
2. **Upload installer**. Progress bar runs to 100%.

You'll see the file listed with its real filename and size:

```
VSCodeSetup-x64-1.94.0.exe   97.5 MiB   just now
```

### A.3 — Add a detection rule

In the **Detection rules** section:

1. **+ Add detection rule**.
2. **Type**: file.
3. **File path**: `%ProgramFiles%\Microsoft VS Code\Code.exe`.

Done. The agent will check whether this file exists on the
target; if yes, "already installed" and the package skips.

The `%ProgramFiles%` token is expanded by the agent on the target
machine — it works for both 64-bit and 32-bit hosts because the
env var resolves correctly on each.

### A.4 — Add the install step

In **Install steps**:

1. **+ Add install step**.
2. **What does this step do?** → `Run an EXE installer`.
3. **Description**: `Install VS Code silently`.
4. **EXE file path**: `VSCodeSetup-x64-1.94.0.exe` — type the
   bare filename. The agent resolves it to the on-disk path
   automatically.
5. **Args**: `/VERYSILENT /MERGETASKS=!runcode`
6. **Success exit codes**: leave at default (`0`).
7. **If this step fails**: "Abort the whole package".
8. Save changes.

### A.5 — Link the package to an image

Back to **Images** → your image → **Software** section → add the
VS Code package. Save.

### A.6 — Deploy a fresh target

PXE-boot a target, pick the image, watch Windows install. After
the first login, the agent fires the package:

- Detection rule says "Code.exe doesn't exist" → not detected.
- Install step runs `setup.exe /VERYSILENT /MERGETASKS=!runcode`.
- Exits 0 → success.
- Re-runs detection → file now exists → package marked installed.

VS Code is on the start menu.

## Example B — Multi-file installer: Microsoft Office with a transform

Office's Click-to-Run setup takes a `setup.exe` plus a
`configuration.xml` that tells it which channel, language, and
products to install. Many shops add a `.mst` transform for
branding too. That's three files for one package.

### B.1 — Create the package

**Software → + New software package** → "Microsoft 365 Apps".

### B.2 — Upload all three files

In **Payload files**:

1. Upload `setup.exe` (the Office Deployment Tool).
2. Upload `configuration.xml`:
   ```xml
   <Configuration>
     <Add OfficeClientEdition="64" Channel="Current">
       <Product ID="O365ProPlusRetail">
         <Language ID="en-US"/>
       </Product>
     </Add>
     <Display Level="None" AcceptEULA="TRUE"/>
   </Configuration>
   ```
3. Upload `oem-branding.mst` (if you have one).

The Payload files table now shows all three:

```
configuration.xml      213 B    just now
oem-branding.mst       22 B     just now
setup.exe              18.7 MiB just now
```

Each row has a copy-filename button — handy when you're filling
in step paths below and want to make sure the name matches.

### B.3 — Add a detection rule

Office writes a registry value when installed. Use a registry
rule:

1. **+ Add detection rule**.
2. **Type**: registry.
3. **Hive**: HKLM.
4. **Key**: `SOFTWARE\Microsoft\Office\ClickToRun\Configuration`.
5. **Value name**: `ProductReleaseIds`.
6. **Value equals**: leave empty (just check existence).

### B.4 — Add the install steps

Three steps, in order:

**Step 1 — stage the transform**

| Field | Value |
|---|---|
| Type | Copy a file |
| Description | Stage OEM branding transform |
| Source path | `oem-branding.mst` |
| Destination path | `C:\Program Files\Common Files\Microsoft Shared\OFFICE16\oem-branding.mst` |

The source `oem-branding.mst` is the bare filename — agent
resolves it to the file you uploaded. The destination is an
absolute path on the target.

**Step 2 — run Office setup with the config**

| Field | Value |
|---|---|
| Type | Run an EXE installer |
| Description | Run Office setup with config |
| EXE file path | `setup.exe` |
| Args | `/configure configuration.xml` |
| Success exit codes | `0` |

Note **both** `setup.exe` and `configuration.xml` resolve to
uploaded files — bare filenames in either the path field or the
args work. The agent rewrites both before executing.

**Step 3 — smoke test (optional)**

| Field | Value |
|---|---|
| Type | Run a PowerShell script |
| Description | Confirm Office package present |
| PowerShell body | `Get-AppxPackage Microsoft.Office.Desktop \| Where-Object { $_.Status -eq 'Ok' }` |
| If this step fails | Continue (this is just a smoke check) |

Save. Link the package to an image. Deploy.

## Path resolution rules — full reference

When the agent runs a step's path field (Source path, MSI path,
APPX path, EXE path), it applies these rules:

| You write | Agent does |
|---|---|
| `setup.exe` (bare filename) | Looks it up in the package's uploaded files. If found, resolves to the absolute path in the per-package work directory. If not found, used verbatim — typo surfaces as "file not found" at run time. |
| `C:\Program Files\Acme\foo.exe` (absolute Windows path) | Used verbatim. |
| `/usr/local/bin/foo` (POSIX absolute) | Used verbatim. |
| `%ProgramFiles%\Acme\foo.exe` (env var) | Expanded by the agent against the target's environment. Works for `%ProgramFiles(x86)%`, `%LOCALAPPDATA%`, `%APPDATA%`, `%SystemRoot%`, etc. |
| `{payload}` (legacy single-file token) | Resolves to the uploaded file when there's exactly one. Ambiguous in multi-file packages — prefer bare filenames. |

The **Destination path** in a copy or unzip step is NOT remapped
— it's where things go on the target, not where they come from.

## Detection rules — full reference

See [reference-detection-rules.md](reference-detection-rules.md)
for the full list: file, registry, MSI product code, script.

## Install steps — full reference

See [reference-install-steps.md](reference-install-steps.md) for
all 7 step types and their fields.

## Common patterns

**An app that's distributed as a ZIP**

1. Upload the zip.
2. Step 1: `Extract a ZIP archive`, Source = `app-bundle.zip`,
   Destination = `C:\Tools\Acme`.
3. Step 2: `Run an EXE installer`, EXE file path =
   `C:\Tools\Acme\setup.exe`, Args as needed.

**An MSI with a transform**

1. Upload `Acme.msi` and `branding.mst`.
2. Step: `Install an MSI`, MSI path = `Acme.msi`, Extra args =
   `TRANSFORMS=branding.mst`.

**A PowerShell-only "install" (e.g. setting a registry value)**

1. Upload nothing (or just a `.ps1`).
2. Step: `Run a PowerShell script`, body inline, or:
3. Step: `Run a PowerShell script`, body =
   `& "$PSScriptRoot\install.ps1"` — uploaded scripts land in
   the per-package work directory which is `$PSScriptRoot` for
   the running step.

## If something didn't work

| Symptom | Fix |
|---|---|
| Package is "installing" every time, never detected | Detection rule is too strict. Test the rule by hand on a deployed target. Most common: a registry value you expected isn't there because the installer was 32-bit (look under `HKLM\SOFTWARE\WOW6432Node\...`). |
| Install step reports exit code N | Check whether the installer actually returns N for success. Add it to **Success exit codes** (e.g. MSIs return `3010` to mean "success, needs reboot"). |
| `file not found` at run time | Your bare filename doesn't match an uploaded file. Filenames are case-sensitive in the lookup. Copy the filename from the Payload files table using the copy button to be sure. |
| Office setup runs but installs the wrong thing | `configuration.xml` likely wasn't found. Verify it's in the Payload files list, and that the step's args literally say `/configure configuration.xml` (the agent rewrites the bare filename). |
| Step body uses `{payload}` and the package now has multiple files | `{payload}` is ambiguous in multi-file packages — agent log says so. Replace with the bare filename of the specific file you meant. |
