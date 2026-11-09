# Proposal: MSI agent install & driver capture from new devices

Status: **proposal — for review, nothing implemented yet.**

Two related features:

1. **Agent MSI.** Install the AutoDeploy agent on a machine that keeps its stock
   OEM image. No PXE and no reimage. The MSI works with Intune, GPO, SCCM or a
   USB stick.
2. **Driver capture.** On a newly purchased device that is still on its OEM
   image, export the drivers the manufacturer installed. Upload them, and have
   AutoDeploy turn them into a driver package with SMBIOS filters already filled
   in. The next PXE deploy to that model then gets the right drivers with no
   manual work.

They fit together. Install the MSI on the first unit of a new model, capture its
drivers, then reimage every unit of that model through AutoDeploy, or leave
them on stock.

---

## 1. What exists today (relevant pieces)

| Area | Current state |
|---|---|
| Manual agent install | `docs/portal/downloads.md`: copy the exe, run `autodeploy-agent.exe --server <url>`, then `install-service`. Manual and elevated, so it does not scale. |
| Enrolment | `ensureEnrolled` (`agent/cmd/autodeploy-agent/main.go`) POSTs `/api/v1/agent/enroll` by SMBIOS identity and saves `ServerURL`/`AgentID` to `HKLM\SOFTWARE\AutoDeploy`. It runs only once, at process start. If it fails, the agent has no `agent_id` and a service start falls through to the legacy `--image-id` path and exits. |
| Agent secrets | `handleAgentCredentialRequest` issues a secret via `media` (image enrol token on USB/ISO), `deploy` (grant), `first-contact` (legacy allowed), `new-machine` (`AgentAutoEnrollNew`, less than 1 h old), or an **operator-approved request**. A machine installed by hand today lands in "approval required" unless `AgentAutoEnrollNew` is on. |
| Enrol tokens | `image_enroll_token` (migrations 0037/0038): one per image, never expires, encrypted for re-export, **bound to an image**. |
| Install location | `SetupComplete.cmd` puts the exe in `%ProgramFiles%\AutoDeploy\autodeploy-agent.exe` and registers service `AutoDeployAgent`. |
| Self-update | The agent replaces its own exe in place (`agent/internal/selfupdate`). |
| Release | `release.yml` builds raw exes only (windows amd64/arm64) on `ubuntu-latest`. There is no installer package. |
| Driver packages | Upload a zip to `drivers/<id>/payload.bin`, then `payload.ExtractDriverPackage` unpacks it, scans INFs and works out which storage drivers go to `$WinPEDriver$`. Filters use `match.AllowedKeys`. "Use a known machine as a filter" already exists. |
| Agent jobs | Bulk job actions dispatched in `main.go` (`toolkit`, `script`, `rename`, `software_push`, `reimage`). Results go to `/api/v1/agent/jobs/{id}/result`. There is **no agent-to-server file upload** path except log shipping. |

---

## 2. Part A — Agent MSI

### 2.1 Goals

- `msiexec /i autodeploy-agent-x64.msi /qn SERVERURL=https://deploy.example.com ENROLLTOKEN=…`
  installs a working, enrolled, secured agent with no operator approval step.
- Works unattended under Intune Win32, GPO software installation, SCCM/MECM, or
  a double-click from a USB stick.
- Clean uninstall: removes the service and files, and leaves the server inventory
  record alone.
- Does not fight with agent self-update.
- Optionally sets what the machine should receive after install (machine group,
  and optionally an image's software set **without reimaging**).

### 2.2 Building the MSI

**Recommendation: WiX Toolset v5 on a `windows-latest` runner**, added as a new
`agent-msi` job in `release.yml` (and a build-only check in `ci.yml`).

- It needs the Go-built exe from the existing `build` matrix, passed as a
  workflow artifact, so the MSI ships the exact binary that is released.
- It produces `autodeploy-agent-windows-amd64.msi` and
  `autodeploy-agent-windows-arm64.msi`, each with `.sha256`/`.version`
  sidecars, so the existing `autodeploy-*` release glob and the Downloads page
  pick them up.
- Sources live under a new `agent/installer/` folder (`Product.wxs`, a small
  `Makefile`/PowerShell build script).
- **Alternative considered:** `wixl` (msitools) on Linux keeps everything on
  Ubuntu runners. But it supports only a subset of WiX 3, has weak
  `ServiceInstall`/custom-action support, and gets little testing in enterprise
  deployment tools. Not recommended.
- **Code signing:** optional Authenticode signing of both the exe and the MSI,
  if `SIGNING_CERT`/`SIGNING_PASSWORD` repo secrets are set. Unsigned builds
  still work but trigger SmartScreen and some Intune policies. *(Open question 1.)*

### 2.3 MSI contents and behaviour

| Item | Detail |
|---|---|
| Install dir | `[ProgramFiles64Folder]AutoDeploy\`, the same as a PXE-deployed machine, so both install routes look identical on disk. |
| Files | `autodeploy-agent.exe`. The credential provider DLL is **not** included, because the setup lock only applies to deploys. |
| Service | Native MSI `ServiceInstall`/`ServiceControl` for `AutoDeployAgent` (auto-start, restart-on-failure recovery actions). Uninstall then removes it reliably, which the `install-service` subcommand can't guarantee. |
| Registry | `HKLM\SOFTWARE\AutoDeploy\ServerURL` from `SERVERURL`. `AgentID` is **not** written; the agent enrols itself. |
| Properties | `SERVERURL` (required, validated with a launch condition), `ENROLLTOKEN` (optional, secret), `INSECURETLS` (optional, dev only), `MACHINEGROUP` / `IMAGEID` (optional, see 2.5; in most cases set by the token instead). |
| Secret handling | `ENROLLTOKEN` is marked `Hidden="yes"` and added to `MsiHiddenProperties`, so it never appears in `msiexec /l*v` logs. A deferred custom action (the agent exe itself: `autodeploy-agent.exe stage-enrol-token`, reading from `CustomActionData`) writes it to `%ProgramData%\AutoDeploy\identity\enroll.json` with a SYSTEM/Administrators-only ACL (the same `secureDir` used for `credential.json`). It is never written to the registry. |
| Upgrades | Fixed `UpgradeCode`, with `MajorUpgrade` allowing any newer MSI to replace an older one. |
| Self-update coexistence | The exe component's **KeyPath is a registry value**, not the file. MSI repair/self-heal therefore never "repairs" a self-updated exe back to the MSI's older version. Self-update remains the normal upgrade route; the MSI is a bootstrapper. Uninstall still removes the file because it is in the component. |
| Uninstall | Stops and deletes the service, removes files and `HKLM\SOFTWARE\AutoDeploy`. It does **not** delete `%ProgramData%\AutoDeploy\identity` by default (so a reinstall keeps the same secret). `PURGE=1` removes it too. The server record is not touched; an optional "agent uninstalled" event is a later improvement. |
| Existing PXE-installed agent | If `AutoDeployAgent` already exists and was not installed by MSI, the MSI takes it over: it stops and replaces the service, and keeps the registry `AgentID` and the identity folder. |

### 2.4 Agent changes needed

1. **Keep retrying enrolment in service mode.** If the service starts with
   `ServerURL` but no `AgentID` (typical right after the MSI, or when the server
   is unreachable at install time), it should retry `ensureEnrolled` with backoff
   inside the SCM handler until it succeeds, then enter `runSelfLoop`. Today it
   would fall into the legacy path and exit, and the SCM would keep restarting it.
2. **Read the token from the new location.** `credManager.Obtain` reads
   `%ProgramData%\AutoDeploy\identity\enroll.json` as well as the existing
   `%WINDIR%\AutoDeploy\enroll.json`. `image_id` becomes optional in that file.
   The file is deleted after a successful issue, as today.
3. **New `stage-enrol-token` subcommand**, used only by the MSI custom action.
   It reads the token from stdin/CustomActionData, never from argv, and writes
   the ACL'd file.
4. **Hardware inventory on first poll.** This already happens via enrol and
   report; it just needs a test confirming that an MSI-installed machine
   inventories before any image is bound.

### 2.5 Server changes: general-purpose install tokens

Generalise `image_enroll_token` into **agent install tokens**:

```sql
-- 0039_agent_install_tokens.sql
CREATE TABLE agent_install_token (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT     NOT NULL,          -- "Intune – Finance laptops"
    token_hash       TEXT     NOT NULL UNIQUE,
    token_cipher     TEXT     NOT NULL DEFAULT '', -- so the portal can show it again
    image_id         INTEGER  REFERENCES image(id) ON DELETE SET NULL,   -- optional software set
    machine_group_id INTEGER  REFERENCES machine_group(id) ON DELETE SET NULL, -- optional
    max_uses         INTEGER  NOT NULL DEFAULT 0, -- 0 = unlimited
    use_count        INTEGER  NOT NULL DEFAULT 0,
    expires_at       DATETIME,                   -- NULL = never
    revoked_at       DATETIME,
    created_by       TEXT     NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- The credential request gains a new `via = "install-token"` branch, checked
  alongside `media`. Validation covers hash match, not revoked, not expired, and
  under `max_uses`. Each use is audit-logged with the token **name** (never its
  value), the machine and the IP.
- Existing image media tokens stay as they are (no behaviour change for
  USB/ISO). They could be moved into this table later.
- **Optional token side effects** on first use:
  - `machine_group_id` adds the machine to that manual group.
  - `image_id` binds the machine to the image **for software only**. The agent
    installs the image's loadout via `/agent/self`. The binding must be flagged
    so it never triggers a reimage or counts as a deploy. *(Open question 2:
    is "software-only binding" the right model, or should a loadout be bindable
    directly without an image?)*
- **Clone/abuse limits:** a token is only as secret as the place it's stored, and
  Intune and GPO both store it in the clear, so:
  - default `max_uses` and expiry are suggested in the UI;
  - a token cannot take over a machine that **already has a secret** (that still
    needs approval, as today). It only covers `legacy`/new machines, so a leaked
    token cannot hijack existing machines;
  - revoking is one click and is audited.

### 2.6 Portal

- **Fleet → Agent install** (new page, or a tab on Downloads):
  - Download buttons for the x64/ARM64 MSI.
  - Create, list and revoke install tokens (name, optional group/image, uses,
    expiry, last used).
  - For a selected token, ready-to-copy snippets for: `msiexec` command line,
    Intune Win32 app (install/uninstall command and detection rule: registry
    `HKLM\SOFTWARE\AutoDeploy\ServerURL` exists, or MSI product code), GPO note
    (GPO can't pass properties, so it needs a server-generated **MST transform**;
    see below), and a PowerShell one-liner that downloads the MSI from the server
    and installs it.
  - **"Download install bundle"**: a zip of the MSI plus `install.cmd` with
    `SERVERURL`/`ENROLLTOKEN` filled in, for USB or manual use, with a clear
    "contains a secret" warning.
- **GPO/MST:** generating an MST server-side means writing an OLE compound
  file in Go. That's possible (e.g. `github.com/richardlehane/mscfb` is
  read-only, so we'd need our own writer). **Recommendation: defer.** For GPO,
  document a startup-script approach using the bundle. *(Open question 3.)*
- Downloads page: recognise `*.msi` in the agent group.
- Machine page: show "Installed via MSI (token *Finance laptops*)" from `issued_via`.

### 2.7 Tests

- Agent: service-mode enrol retry; token file in the new location; token
  without `image_id`; `stage-enrol-token` writes an ACL'd file and never logs
  the value (extend `credentials_test.go`, `enroll_test.go`).
- Server: install token validity matrix (revoked, expired, max uses, already
  secured machine is refused), group/image side effects, audit events without the
  token value (`agentcred_handlers_test.go`). Also `scripts/check-secrets.sh`
  coverage for `token` names.
- CI: build the MSI on `windows-latest`, then `msiexec /i … /qn` into the runner
  against a stub server URL. Assert the service is registered and the registry
  set, then uninstall and assert everything is cleaned up.
- E2E (manual/lab, extending `scripts/e2e`): install on a stock Win11 VM and
  check it appears in Machines, is secured without approval, and gets the
  group's software.

---

## 3. Part B — Build driver packages from a device

### 3.1 Workflow

1. A new model arrives. Install the agent with the MSI (Part A) on one unit,
   leaving the OEM image in place. Ideally run the OEM's update tool (Dell
   Command Update, Lenovo System Update, HP Image Assistant) first so the
   drivers are current.
2. On the machine's page, click **Capture drivers** (or tick several machines
   and run the bulk action, one per model).
3. The agent exports the third-party driver store, zips it and uploads it.
4. The server creates a **draft** driver package, named for example
   `Dell Inc. Latitude 5550 (captured 2026-11-09)`. Its SMBIOS filter comes from
   the machine (manufacturer + product, or base board for white-box machines,
   using the existing "known machine as filter" logic). The server unpacks it
   and scans the INFs with the existing `ExtractDriverPackage`, including the
   `$WinPEDriver$` storage split.
5. The operator gets a notification ("Driver package captured — review"),
   reviews the INF list and filter, removes unwanted classes, and **publishes**
   it. Only published packages are matched at deploy time.

### 3.2 Agent: new job action `driver_capture`

- Payload: `{ "exclude_classes": ["Printer","Net/VPN",…], "include_inbox": false }`.
- Export with `pnputil /export-driver * <dir>` (Windows 10 1607+), falling back
  to `Export-WindowsDriver -Online -Destination <dir>`. Both export only
  **third-party (OEM) drivers**, which is what we want; inbox drivers are already
  on the install media.
- Read each exported INF's `Class`/`ClassGuid`, drop excluded classes (printers,
  virtual adapters and VPN/AV filter drivers by default), and write a
  `capture.json` manifest (INF name, class, provider, version, date, original
  device instance IDs).
- Check free space first (an export can be 1–3 GB with GPU drivers). Zip into
  the work dir, compute SHA-256, upload (3.3), then clean up. Report the outcome
  through the existing job result endpoint.
- Runs as SYSTEM (the service's account). Nothing interactive.

### 3.3 Server: authenticated upload endpoint

`PUT /api/v1/agent/driver-capture/{job_id}`

- Authenticated with the agent's id and secret (the existing `agentAuth`). It
  only accepts uploads for a `driver_capture` job that is **open and assigned to
  this machine**, and only one upload per job, so an agent cannot push arbitrary
  driver packages.
- Streams to `drivers/<new id>/payload.bin` with `Blobs.WriteStream` (no
  buffering), enforces a configurable size limit (default 4 GiB), and checks
  `X-Content-SHA256`.
- Supports `Content-Range` resume for large captures over poor links; the
  conventions already require Range support for downloads. *(Could be phase 2
  if plain retry is acceptable at first.)*
- Then runs `ExtractDriverPackage` (which already protects against zip-slip;
  confirm in review) and creates the draft package, its filter and its notification.

### 3.4 Data model

```sql
-- 0040_driver_capture.sql
ALTER TABLE driver_package ADD COLUMN status TEXT NOT NULL DEFAULT 'published'; -- draft | published
ALTER TABLE driver_package ADD COLUMN source TEXT NOT NULL DEFAULT 'upload';    -- upload | capture
ALTER TABLE driver_package ADD COLUMN captured_from_machine_id INTEGER
    REFERENCES machine_record(id) ON DELETE SET NULL;
ALTER TABLE driver_package ADD COLUMN captured_at DATETIME;
```

- `resolve` skips `status = 'draft'` packages (with a unit test in
  `resolve/drivers_test.go`). Existing packages default to `published`, so
  nothing changes for current users.
- **Overlap check:** when the draft is created, run the existing matcher against
  the source machine's identity. If a published package already matches,
  say so on the review page and offer **"Replace contents of *X*"** (keep X's
  filters, swap its payload) as well as "Publish as new package". This handles
  refreshing a model's drivers.

### 3.5 Portal

- Machine page: **Capture drivers** button, with an options dialog for excluded
  classes, plus capture history (job status and link to the resulting package).
- Bulk operations: **Capture drivers** action.
- Drivers list: Draft / Captured badges, and a filter for drafts.
- Driver edit page for a draft: source machine, capture manifest, overlap
  warning, **Publish** / **Replace existing** / **Discard**.
- **"Models without drivers"** report (Drivers page or Dashboard): models in
  inventory that no published package matches, each with a "Capture from…"
  shortcut that picks an online machine of that model.

### 3.6 Optional: automatic capture

A setting, **off by default**: *"When a new hardware model enrols through an
install token and no driver package matches it, capture its drivers
automatically."* This queues a `driver_capture` job for the first machine of an
unmatched model. The result is still a **draft**, so nothing is applied without
operator review. This ties Part A and Part B together for a hands-off "buy a
new model, enrol one, get a driver package" flow.

### 3.7 Tests

- Agent: INF class parsing and filtering, manifest generation, a free-space
  guard, and the upload client (with fake pnputil output; `_other.go` stubs for
  non-Windows CI).
- Server: upload auth matrix (wrong machine, closed job, second upload, oversize,
  bad hash), draft creation with seeded filter, resolver ignores drafts, the
  overlap check, and replace-existing.
- Lab: capture on a real OEM laptop, publish, then PXE-deploy the same model and
  confirm Device Manager is clean.

### 3.8 Out of scope (possible follow-ups)

- Downloading OEM driver packs directly from Dell/HP/Lenovo catalogues (similar
  to the existing MS Catalog integration). This complements capture, which
  needs one physical unit.
- Diffing two captures of the same model.
- Capturing drivers from a machine **after** an AutoDeploy deploy, to find what
  the current package is missing.

---

## 4. Delivery phases

Each phase is a separate PR, with docs updated in the same PR (`downloads.md`,
new `docs/install/agent-msi.md`, `payloads.md`, `security.md`, `api.md`).

| # | Phase | Depends on |
|---|---|---|
| 1 | Agent: service-mode enrol retry, token file in `%ProgramData%`, `stage-enrol-token` | — |
| 2 | Server: `agent_install_token` table, credential branch, portal token management | 1 |
| 3 | MSI: WiX sources, release/CI jobs, install/uninstall CI test, Downloads + Agent install page, bundle zip | 1, 2 |
| 4 | Driver capture: `driver_package.status/source`, resolver ignores drafts, upload endpoint, `driver_capture` agent job | — (works with any resident agent) |
| 5 | Driver capture UX: machine/bulk action, review/publish/replace, "Models without drivers" | 4 |
| 6 | Optional auto-capture on new model enrol | 2, 4 |

Phases 1–3 and 4–5 are independent and can proceed in parallel.

---

## 5. Open questions for review

1. **Code signing:** is there an Authenticode certificate to sign the exe and
   MSI in CI? Without one, Intune works but SmartScreen will warn on a manual
   double-click install.
2. **What does "leave on stock image" receive?** Options:
   (a) nothing beyond inventory, jobs and Windows Updates;
   (b) a machine group only, with software pushed by bulk ops;
   (c) a software-only binding to an image's loadout (proposed as optional per
   token). Which do you want as the default?
3. **GPO support:** is a startup-script approach enough, or do you need a real
   per-token MST transform (more work, server-side OLE writer)?
4. **ARM64 MSI:** ship it now alongside x64, or x64 only to start?
5. **Token defaults:** should new install tokens expire by default (e.g. 30 days
   or N uses), or never expire like media tokens?
6. **Driver capture exclusions:** is the proposed default (exclude printers,
   virtual and VPN/filter drivers, include everything else OEM) right? Should
   captures always go through draft review, or may a trusted operator publish
   in one step?
7. **Upload limits:** is 4 GiB the right maximum capture size, and do mirrors
   need to receive captured packages straight away (they will through the
   normal payload sync)?
