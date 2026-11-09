# Proposal: MSI agent install & driver capture from new devices

Status: **proposal — for review, nothing implemented yet.**

Two related features:

1. **Agent MSI, built by the server.** The server builds an MSI with the server URL and an install token already inside, so it can be deployed by GPO. It installs the AutoDeploy agent on a machine that keeps its stock
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

## 2. Part A — Agent MSI, built by the server

### 2.1 Goals

- **The server builds the MSI** with the server URL and a sign-up (install)
  token already inside. The result is one self-contained `.msi` that needs **no
  command-line properties**, so it works with **GPO Software Installation**
  (which can't pass properties), as well as Intune, SCCM and double-click.
- Installing it gives a working, enrolled, secured agent with no operator
  approval step.
- With no commercial code-signing certificate, the server can **sign the MSI
  with a certificate that Active Directory makes trusted** on domain machines
  (§2.7).
- Clean uninstall, and no conflict with agent self-update.
- Optionally sets what the machine should receive after install (machine group,
  and optionally an image's software set **without reimaging**).

### 2.2 How the server builds the MSI

**Approach: a template MSI from CI, patched by the server.** Building an MSI
from scratch in Go (tables, cabinet, sequences) is a large job. Patching a
known-good template is small and safe.

1. **Template (CI, at release time).** WiX Toolset v5 on a `windows-latest`
   runner builds `autodeploy-agent-windows-amd64.msi`. It contains the
   release's agent exe, and `SERVERURL` / `ENROLLTOKEN` are present in the
   `Property` table but blank. It ships as a release asset, like the agent exe.
   - `install-linux.sh` / `install-windows.ps1` copy it into the server's
     downloads directory.
   - **Settings → Updates** accepts a new template alongside a new agent build.
   - The template on its own is still a valid generic MSI: you pass
     `SERVERURL=… ENROLLTOKEN=…` on the command line, as in the first draft,
     which is handy for Intune.
2. **Per-token build (server, on demand).** New package
   `server/internal/msibuild`:
   - Opens the template. An MSI is an OLE Compound File, so this means reading
     its streams and decoding the MSI stream-name encoding.
   - Rewrites the string pool (`_StringPool` / `_StringData`) and the `Property`
     table rows to set:
     - `SERVERURL`: the server's public URL from Settings, which the operator
       can change per token;
     - `ENROLLTOKEN`: the token;
     - `ProductCode`: a deterministic UUIDv5 of (token id, template version).
   - Updates `\x05SummaryInformation`: a new **package code** (revision
     number) per build, and a Comments field with the token name and build date.
   - Writes a new compound file, then signs it if a signing certificate is
     configured (§2.7).
   - Caches the result under `<data>/agent-msi/<token-id>/`. The cache is
     rebuilt automatically when the token, the template or the signing
     certificate changes.
3. **Library choice.** Look at `github.com/sassoftware/relic` (Apache-2.0, pure
   Go) first. It has a compound-document reader/writer (`lib/comdoc`) **and**
   Authenticode MSI signing (`lib/authenticode`), so one dependency covers both
   patching and signing. It also keeps the server pure Go: no WiX, msitools or
   osslsigncode on the server host, and it still works on the Windows/macOS
   server builds. Per the conventions this dependency is justified because the
   standard library has nothing for either job. Fallback if relic doesn't fit:
   a small in-house CFB writer (about 500 lines) plus `osslsigncode` for
   signing. *(To confirm with a spike before Phase 3.)*
4. **Upgrades.** All builds share one `UpgradeCode`. `MajorUpgrade` with
   `AllowSameVersionUpgrades="yes"` lets a package for a new token or a new
   version cleanly replace an existing install. GPO "redeploy" or "upgrade" then
   works as expected.
5. **Verification in CI.** Server tests patch a template, then check it with
   `msiinfo`/`msidump` (msitools, used only in CI tests). The Windows CI job
   then installs a **server-generated** MSI with `msiexec /i … /qn`, with no
   properties, and asserts the service is installed, the registry has the
   embedded URL and `enroll.json` exists. Finally it uninstalls and checks that
   everything is cleaned up.

### 2.3 MSI contents and behaviour

| Item | Detail |
|---|---|
| Install dir | `[ProgramFiles64Folder]AutoDeploy\`, the same as a PXE-deployed machine. |
| Files | `autodeploy-agent.exe`. The credential provider DLL is **not** included, because the setup lock only applies to deploys. |
| Service | Native MSI `ServiceInstall`/`ServiceControl` for `AutoDeployAgent` (auto-start, restart-on-failure recovery actions). |
| Registry | `HKLM\SOFTWARE\AutoDeploy\ServerURL` from `SERVERURL`. `AgentID` is not written; the agent enrols itself. |
| Token handling | `ENROLLTOKEN` is `Hidden="yes"` and in `MsiHiddenProperties`, so it never appears in `msiexec /l*v` logs. A deferred custom action (`autodeploy-agent.exe stage-enrol-token`, which reads from `CustomActionData`, never argv) writes it to `%ProgramData%\AutoDeploy\identity\enroll.json` with a SYSTEM/Administrators-only ACL. It is never written to the registry. |
| Properties override | Values given on the command line override the embedded ones, as always with MSI, so one built MSI can be pointed elsewhere for testing. |
| Self-update coexistence | The exe component's **KeyPath is a registry value**, not the file, so MSI repair/self-heal never puts a self-updated exe back to the MSI's version. Self-update stays the normal way the agent upgrades. GPO only needs a new package when you change token or server URL. |
| Uninstall | Removes the service, files and `HKLM\SOFTWARE\AutoDeploy`. Keeps `%ProgramData%\AutoDeploy\identity` by default (so a reinstall keeps the same secret); `PURGE=1` removes it. The server record is not touched. |
| Existing PXE-installed agent | Takes over the existing `AutoDeployAgent` service and keeps the registry `AgentID` and identity folder. |
| GPO specifics | GPO installs at computer startup as SYSTEM, from a UNC share. Documented prerequisites: share readable by *Domain Computers*, "Always wait for the network at computer startup and logon" enabled, assign (not publish) to computers. |

### 2.4 Agent changes needed

1. **Keep retrying enrolment in service mode.** If the service starts with
   `ServerURL` but no `AgentID` (typical after a GPO install at boot, before the
   network is fully up), it should retry `ensureEnrolled` with backoff inside the
   SCM handler, then enter `runSelfLoop`. Today it would fall into the legacy
   path and exit.
2. **Read the token from the new location.** `credManager.Obtain` reads
   `%ProgramData%\AutoDeploy\identity\enroll.json` as well as
   `%WINDIR%\AutoDeploy\enroll.json`. `image_id` becomes optional. The file is
   deleted after a successful issue.
3. **New `stage-enrol-token` subcommand** for the MSI custom action.
4. **Report domain membership early.** The agent already reports name and AD
   path. Make sure the first report after enrolling carries the joined domain
   and computer name, because GPO tokens rely on them (§2.5).

### 2.5 Server: install (sign-up) tokens

```sql
-- 0039_agent_install_tokens.sql
CREATE TABLE agent_install_token (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT     NOT NULL,           -- "GPO – Finance OU"
    token_hash       TEXT     NOT NULL UNIQUE,
    token_cipher     TEXT     NOT NULL DEFAULT '', -- secrets.Box, needed to rebuild the MSI
    server_url       TEXT     NOT NULL DEFAULT '', -- '' = server default
    image_id         INTEGER  REFERENCES image(id) ON DELETE SET NULL,        -- optional software set
    machine_group_id INTEGER  REFERENCES machine_group(id) ON DELETE SET NULL, -- optional
    require_ad       INTEGER  NOT NULL DEFAULT 0,  -- machine must have an AD computer account
    max_uses         INTEGER  NOT NULL DEFAULT 0,  -- 0 = unlimited
    use_count        INTEGER  NOT NULL DEFAULT 0,
    expires_at       DATETIME,                    -- NULL = never
    revoked_at       DATETIME,
    created_by       TEXT     NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- The credential request gets a new `via = "install-token"` branch next to
  `media`. Each use is audit-logged with the token **name** (never its value),
  the machine and the IP.
- **Side effects on first use:** `machine_group_id` adds the machine to that
  manual group. `image_id` binds the machine for **software only**, never a
  reimage or a deploy. *(Open question 1.)*
- **The token sits inside the MSI, so treat it as readable.** Anyone who can
  read the GPO share, or the copy Windows Installer caches on each machine, can
  extract it. Protections:
  - a token can **never** take over a machine that already has a secret. It only
    covers new or legacy machines; anything else still needs approval, as today;
  - **`require_ad`** (default **on** for GPO tokens): before issuing a secret,
    the server uses the existing AD integration (`addomain.Directory.FindComputer`)
    to check that the computer name the machine reports exists as a computer
    object in the configured domain. A non-domain machine that copies the MSI
    gets nothing. This narrows the risk but does not remove it, because a
    computer could claim the name of an AD object that hasn't enrolled yet. Such
    a request falls back to operator approval and raises a notification;
  - one-click revoke, which also deletes the cached MSI, plus optional
    expiry and maximum uses. Rotating a token means building a new MSI and
    redeploying it in GPO.

### 2.6 Portal

**Fleet → Agent install** (new page):

- Token list showing name, server URL, group/image, `require_ad`, uses/limit,
  expiry and last used, plus actions: **Create**, **Revoke**, **Rotate**.
- Per token:
  - **Download MSI (x64)**: built or served from cache, signed if a certificate
    is configured, with a "contains an install token" warning;
  - SHA-256 of the built MSI;
  - a step-by-step GPO guide: copy the file to a share, create a GPO under
    Computer Configuration → Software Installation → Assigned, and turn on
    wait-for-network;
  - copy-ready snippets for Intune and msiexec;
  - a PowerShell one-liner that downloads from the server with an API session.
- The MSI download route is portal-authenticated and audited (`agent.msi.download`).
- Downloads page: also lists the generic template MSI.
- Machine page: "Installed via MSI (token *GPO – Finance OU*)".

### 2.7 Signing without a commercial certificate (trust through Active Directory)

**What signing achieves here.** GPO Software Installation does **not** require
a signed MSI, so GPO deployment works unsigned from day one. Signing adds a
verified publisher instead of "Unknown publisher" on manual installs, lets
AppLocker/WDAC publisher rules allow it, and proves the file came from your
AutoDeploy server. Domain machines can be made to trust an internal certificate
through AD, so a public CA isn't needed.

**Settings → Code signing** (new). There are two ways to get a certificate:

- **A. Use your AD Certificate Services (enterprise CA)**, if you have one.
  Issue a certificate from the built-in *Code Signing* template (or a copy of
  it) and **upload the PFX** to AutoDeploy. Domain machines already trust the
  enterprise root through AD. The only extra step is publishing the signing
  certificate to **Trusted Publishers** with GPO, which AppLocker,
  PowerShell `AllSigned` and similar features need.
- **B. AutoDeploy generates its own.** One click creates a private **AutoDeploy
  code-signing root** (long-lived, say 10 years) and a code-signing leaf it
  issues (say 3 years, renewable). The portal offers the root `.cer` for
  download, and the guide gives the two AD ways to trust it:
  - **GPO:** Computer Configuration → Policies → Windows Settings → Security
    Settings → Public Key Policies → *Trusted Root Certification Authorities*
    (root) and *Trusted Publishers* (leaf);
  - **or publish into AD itself** (needs Enterprise Admin), after which every
    domain member trusts it automatically with no GPO:

    ```bash
    certutil -dspublish -f AutoDeploy-CodeSigning-Root.cer RootCA
    ```

AutoDeploy won't write to the AD Configuration partition itself, even though
it has an AD integration. That needs Enterprise Admin, and the AD service
account (which only creates and renames computer objects) shouldn't have it.
The portal produces the files and exact commands, and an admin runs them.

**Implementation notes:**

- The private key is stored encrypted with `secrets.Box` (the existing
  `AUTODEPLOY_SECRETS_KEY`). It is never logged, never downloadable after
  import or generation, and every use is audit-logged. `check-secrets.sh`
  patterns are extended to cover `pfx`/`privateKey`.
- MSI Authenticode signing in pure Go (relic, §2.2): SHA-256 digest, with an
  optional **RFC 3161 timestamp URL**. Without a timestamp the signature stops
  verifying when the leaf expires, so AutoDeploy re-signs cached MSIs
  automatically on renewal and warns 30 days before expiry. Option B's leaf
  can renew itself from the stored root.
- Changing the certificate invalidates the MSI cache, so the next download
  is re-signed.
- **Signing the agent exe too** (for AppLocker/WDAC exe rules) is a
  follow-up. The server would sign staged agent exes (and advertise the
  signed file's SHA-256 to self-update), and repack the exe into the MSI
  template's cabinet, which means writing a store-only CAB. Not needed for
  GPO install. *(Open question 3.)*
- **SmartScreen caveat:** an internally trusted signature does not build
  SmartScreen reputation for files downloaded from the internet. That
  doesn't matter for GPO, share or Intune installs, which carry no Mark of
  the Web.

### 2.8 Tests

- Agent: service-mode enrolment retry, token file in the new location, token
  without `image_id`, and `stage-enrol-token` writes an ACL'd file and never
  logs the value.
- Server:
  - `msibuild` round-trip: patch, re-read, compare `Property` values, check the
    package code changes and every other stream is byte-identical;
  - string-pool edge cases (long-ID pools, refcounts);
  - a corrupt or missing template gives a clear error;
  - signing: the signature verifies (relic verify, plus `osslsigncode verify`
    in CI) and a certificate change invalidates the cache;
  - token matrix: revoked, expired, max uses, already-secured machine refused,
    and `require_ad` against `addomain.FakeDirectory`;
  - audit events never contain the token or key.
- CI (Windows): install a server-built MSI with no properties, both unsigned
  and signed by a test root added to the runner's store, then check the
  service, registry and token file, and uninstall.
- Lab: a GPO-assigned install on a domain-joined stock Windows 11 VM. Check it
  enrols and is secured without approval on first boot, and that a
  non-domain VM with the same MSI is held for approval.

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
new `docs/install/agent-msi.md` with the GPO walkthrough, new
`docs/operations/code-signing.md`, `payloads.md`, `security.md`, `api.md`).

| # | Phase | Depends on |
|---|---|---|
| 0 | Spike: confirm relic (or the fallback) can patch the `Property` table of a WiX-built MSI and produce one that installs and verifies on Windows | — |
| 1 | Agent: service-mode enrolment retry, token file in `%ProgramData%`, `stage-enrol-token`, early domain report | — |
| 2 | Server: `agent_install_token` table, `install-token` credential branch with `require_ad`, token management UI | 1 |
| 3 | Template MSI (WiX in CI and release) and server-side `msibuild`, per-token MSI download, GPO docs, Windows CI install test | 0, 1, 2 |
| 4 | Code signing: Settings → Code signing (PFX import / generate root + leaf), MSI signing, `.cer` download and AD trust guide, renewal | 3 |
| 5 | Driver capture: `driver_package.status/source`, resolver ignores drafts, upload endpoint, `driver_capture` agent job | — (works with any resident agent) |
| 6 | Driver capture UX: machine/bulk action, review/publish/replace, "Models without drivers" | 5 |
| 7 | Optional auto-capture on new-model enrolment | 2, 5 |
| — | Follow-up: sign the agent exe as well (staged updates + MSI cabinet repack) | 4 |

Phases 0–4 and 5–6 are independent and can proceed in parallel. GPO
deployment is usable at the end of phase 3, and signing (phase 4) adds trust
on top.

---

## 5. Open questions for review

Already settled: the MSI is built by the server with the token embedded (for
GPO), and there's no commercial certificate, so trust comes from AD (§2.7).

1. **What does a machine left on its stock image receive by default:** (a)
   nothing beyond inventory, jobs and Windows Updates; (b) a machine group only;
   (c) a software-only binding to an image's loadout (proposed as optional per
   token)?
2. **Certificate source:** do you have AD Certificate Services (option A), or
   should AutoDeploy generate its own root (option B)? Proposal: build both,
   with B as the default when no PFX is uploaded.
3. **Exe signing:** do you run AppLocker or WDAC with publisher rules? If so,
   the follow-up that signs the agent exe moves up into phase 4.
4. **`require_ad` default:** on for every token, or only for tokens marked
   "for GPO"? (Off allows Intune or Autopilot devices that aren't AD-joined.)
5. **ARM64 MSI:** x64 only to start (proposed), with ARM64 later?
6. **Token defaults:** should new install tokens expire by default, or never
   expire (normal for a GPO token, protected by `require_ad` and revoke)?
7. **Driver capture exclusions:** is the proposed default (exclude printers,
   virtual and VPN/filter drivers) right, and should captures always go
   through draft review?
8. **Upload limits:** is 4 GiB the right maximum capture size?
