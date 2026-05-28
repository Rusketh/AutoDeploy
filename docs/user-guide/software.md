# Software packages

> **Status.** Phase 6. SoftwarePackage detection rules and ordered install
> steps are typed and validated server-side; the agent evaluates detection
> and executes steps on the target. Software loadouts (Phase 7) layer
> additional ordered collections on top.

## Package shape

```json
{
  "name": "acme-suite-2026",
  "description": "ACME Suite",
  "detection_json": "[ ...rules... ]",
  "steps_json":     "[ ...steps... ]"
}
```

Upload the installer payload separately:

```sh
curl -X PUT --upload-file ./acme-2026.msi \
    http://127.0.0.1:8080/api/v1/software/1/upload
```

## Detection rules

A package is treated as **already installed** when **every** detection rule
reports present (conservative AND). Zero rules means **install every time**
— the agent emits a warning so this is visible.

| `type`     | Required fields                                                                                |
|------------|------------------------------------------------------------------------------------------------|
| `file`     | `file_path`. Optional `file_version`, `file_sha256` for stricter equality.                     |
| `registry` | `registry_hive`, `registry_key`. Optional `registry_value` + `registry_equals` for value compare. |
| `msi`      | `msi_product_code` — the `{GUID}` form Windows uses.                                            |
| `script`   | `script_shell` (`cmd` or `powershell`) and `script_body`. Exit code 0 = detected.               |

Unknown types are rejected at save time.

## Install steps

Steps run in array order. Each step's `success_codes` (default `[0]`) and
`continue_on_failure` (default `false`) decide the agent's reaction:

- Exit code is in `success_codes` → step succeeded; move on.
- Exit code is **not** in `success_codes` and `continue_on_failure` is
  `false` → the package is **aborted**; the agent reports the failure
  and does not run subsequent steps.
- Exit code is not in `success_codes` and `continue_on_failure` is
  `true` → the agent logs the failure and moves on.

| `type`        | Required fields                                                            |
|---------------|----------------------------------------------------------------------------|
| `copy`        | `source_path`, `destination_path`.                                          |
| `msi`         | `msi_path`. `msi_args` is appended after `/quiet /norestart`.               |
| `appx`        | `appx_path`. Installed via `Add-AppxPackage`.                                |
| `cmd`         | `script_body`. Runs in `cmd /C`.                                             |
| `powershell`  | `script_body`. Runs via `powershell -NoProfile -NonInteractive`, body piped to stdin. |
| `exe`         | `exe_path`. `exe_args` is passed verbatim.                                  |

### The `{payload}` token

The literal string `{payload}` in `source_path`, `msi_path`, `appx_path` or
`exe_path` is replaced by the on-disk path of the package's downloaded
installer at run time. This means you don't have to know where the agent
chose to land the file:

```json
"steps_json": "[
  {\"type\":\"msi\", \"msi_path\":\"{payload}\", \"success_codes\":[0,3010]}
]"
```

## What the agent does

For each package in the effective software set, in order:

1. Evaluate detection rules. If all report present, log
   `package.skip` and move on.
2. Otherwise, download the package's installer payload to the work
   directory.
3. Substitute `{payload}` in every step path.
4. Execute steps in order, honouring `success_codes` and
   `continue_on_failure`.
5. Log `package.install.ok` or `package.install.fail` for the package.

The agent's per-step log captures the step type, exit code and whether
it aborted — enough to diagnose a failed install centrally once the
log-collection layer (Phase 14) is in place.

## Endpoints

```
POST /api/v1/agent/software       # agent → server: returns effective list
GET  /payload/software/{id}       # agent ← server: streams the installer
POST /api/v1/software             # operator → server: create package
PUT  /api/v1/software/{id}        # operator → server: update package
PUT  /api/v1/software/{id}/upload # operator → server: upload installer
```
