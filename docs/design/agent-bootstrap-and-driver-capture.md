# Proposal: bootstrap install for stock-image machines & driver capture

Status: **proposal, revision 3, for review. Nothing implemented yet.**

> **Changes in this revision.** The MSI and code-signing plan (revisions 1–2)
> has been **dropped**: a certificate adds complexity and a point of failure.
> Part A now builds on what the agent can already do (enrol, rename, join AD
> through the agent), adds a scripted one-line install, and closes the gaps
> found in the manual route. Part B (driver capture) is unchanged apart from
> referring to the new install route.

Two related features:

1. **Bootstrap install for stock-image machines.** One elevated command puts
   the agent on a machine that keeps its OEM image. The machine is enrolled,
   named, joined to AD in the right OU and groups, and given its software. No
   PXE, no reimage, no MSI, no certificate. It works by hand, from a GPO startup
   script or from Intune.
2. **Driver capture.** On a newly purchased device that is still on its OEM
   image, export the drivers the manufacturer installed. Upload them, and have
   AutoDeploy turn them into a draft driver package with SMBIOS filters already
   filled in.

They fit together. Bootstrap the first unit of a new model, capture its
drivers, then either reimage that model through AutoDeploy or leave it on stock.

---

## 1. What exists today (relevant pieces)

| Area | Current state |
|---|---|
| Manual install | `docs/portal/downloads.md`: copy the exe, run `autodeploy-agent.exe --server <url> [--image-id N]`, then `install-service`. Done by hand and elevated. |
| Enrolment | `ensureEnrolled` (`agent/cmd/autodeploy-agent/main.go`) POSTs `/api/v1/agent/enroll` by SMBIOS identity and saves `ServerURL`/`AgentID` to `HKLM\SOFTWARE\AutoDeploy`. It runs once at process start. If it fails, a service start falls through to the legacy path and exits. |
| `--image-id` | Reports an in-progress deployment, which binds the image (`EnsureBindingImage`), installs the image's software, then closes the deployment. It does **not** join AD (that only happens in the service's self loop). |
| Agent secret | With `AUTODEPLOY_AGENT_AUTO_ENROLL_NEW=true` (the default), a machine the server first saw less than an hour ago gets a secret automatically. Otherwise it needs operator approval. |
| AD join (agent) | `maybeDomainJoin` → `/api/v1/agent/domain-join` returns domain, OU (binding `TargetOU`, else the image's OU) and join credentials, **only if the machine's bound image has agent join enabled**. It runs after `maybeRename` applies the binding's desired name. `Add-Computer` creates the computer object itself. |
| AD pre-staging | `addomain.PrepareComputer` (deletes any same-name object, creates it in the OU, sets the **AD groups**) runs **only on the PXE path** (`payload/manifest.go:423`). An agent join on a stock machine therefore never gets the binding's AD groups. |
| Identity sync | Each poll the agent reports its observed name and DN, and `SyncBindingFromObserved` copies them into the binding. A stock machine's OEM name can therefore end up in its binding if nothing set one first. |
| Boot menu | A bound image is offered as "R) Re-image" and only deploys unattended when a remote re-image is flagged. `docs/portal/machines.md` wrongly says it deploys automatically on the next network boot. Images with no ISO still appear in the menu. |
| Agent download | `GET /api/v1/agent/download/{name}` serves agent binaries from the downloads dir with no login needed (used by self-update). |

---

## 2. Part A — Bootstrap install for stock-image machines

### 2.1 Goals

- **One elevated command** installs, enrols and configures the agent:

  ```
  powershell -ExecutionPolicy Bypass -File Install-AutoDeployAgent.ps1
  ```

  or a `.cmd` equivalent for places PowerShell scripts are blocked.
- The machine ends up **named, in the right OU, in the right AD groups,
  domain-joined, with its software**, all by AutoDeploy, with no manual AD steps.
- The same command works by hand, as a **GPO computer startup script**, or as an
  **Intune** script or Win32 app.
- No MSI, no code-signing certificate, and no new tools on the server.
- A stock machine is **never reimaged** as a side effect.

### 2.2 How it works

```
 Install-AutoDeployAgent.ps1 (≈30 lines)          AutoDeploy server
 ───────────────────────────────────────          ─────────────────
 1. GET /api/v1/agent/bootstrap-info ──────────►  file name + SHA-256 of the current agent
 2. download agent exe, verify SHA-256
 3. run: autodeploy-agent.exe install
          (server URL + profile key from
           env var, never on the command line)
     ├─ copy itself to Program Files\AutoDeploy
     ├─ write HKLM\SOFTWARE\AutoDeploy\ServerURL
     ├─ POST /agent/enroll {identity, install_key} ─►  validate key → apply the
     │                                                profile's binding to this
     │                                                machine and issue a secret
     └─ install + start the AutoDeployAgent service
 4. service self loop (existing code):
     rename to binding name → reboot
     POST /agent/domain-join ─────────────────────►  pre-stage the AD object in
                                                     the OU + groups, return the
                                                     join credentials
     Add-Computer → reboot
     install the image's software, then resident as normal
```

Almost all of the logic lives in the **agent exe** (Go, unit-tested, shared with
PXE installs). The script only fetches and checks the exe, so it is short
enough to read in full before putting it in a GPO.

### 2.3 Install profiles (server)

An **install profile** holds the answers an operator currently gives by hand
between the two manual commands:

```sql
-- 0039_agent_install_profiles.sql
CREATE TABLE agent_install_profile (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT     NOT NULL UNIQUE,     -- "Finance laptops"
    key_hash         TEXT     NOT NULL UNIQUE,     -- SHA-256 of the profile key
    key_cipher       TEXT     NOT NULL DEFAULT '', -- secrets.Box, so the portal can show the script again
    image_id         INTEGER  REFERENCES image(id) ON DELETE SET NULL, -- software + AD join settings
    name_template    TEXT     NOT NULL DEFAULT '', -- e.g. FIN-%serial(6)%
    target_ou        TEXT     NOT NULL DEFAULT '', -- '' = the image's OU
    ad_groups        TEXT     NOT NULL DEFAULT '', -- one per line
    machine_group_id INTEGER  REFERENCES machine_group(id) ON DELETE SET NULL,
    approval         TEXT     NOT NULL DEFAULT 'auto', -- auto | operator
    expires_at       DATETIME,
    max_uses         INTEGER  NOT NULL DEFAULT 0,  -- 0 = unlimited
    use_count        INTEGER  NOT NULL DEFAULT 0,
    revoked_at       DATETIME,
    created_by       TEXT     NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- Binding waiting for operator approval (approval = 'operator').
ALTER TABLE machine_record ADD COLUMN pending_profile_id INTEGER
    REFERENCES agent_install_profile(id) ON DELETE SET NULL;
```

**Enrolling with a key.** `/api/v1/agent/enroll` accepts an optional
`install_key`. When it is valid (not revoked or expired, under `max_uses`):

- **Only new machines.** A key applies only to a machine with **no agent
  secret and no bound image**. A key can never re-target or take over an
  existing machine; for those the key is ignored and enrolment carries on as
  today.
- **`approval = auto`:** in one transaction, **before** the agent's first
  identity report (so `SyncBindingFromObserved` can't copy the OEM name into
  the binding), the server:
  - binds the profile's image, name template, OU and AD groups;
  - adds the machine to the machine group;
  - issues the agent secret (`issued_via = "install-profile"`).
- **`approval = operator`:** the machine is enrolled but stays unbound, with
  `pending_profile_id` set. The secret request is pending, and the
  notification and machine page say *"Enrolled with profile **Finance
  laptops**. Approve to apply it."* Approving issues the secret **and** applies
  the binding. Until then the machine has no bound image, so
  `/agent/domain-join` gives it **no join credentials**.
- Each use is audit-logged (`agent.install_profile.used`) with the profile
  **name**, machine and IP, never the key. A **"Machine enrolled via
  profile"** notification is sent.

**What the key exposes.** The key is in the script, so treat it as readable
by anyone who can read the GPO share or the Intune script. Someone holding it
can enrol a new machine, and with `auto`, receive the image's **join account
credentials** and software. That is the same trust the deployment access PIN
gives PXE today, and the join account is already meant to be least-privilege
(join-only rights on the target OU). Protections:

- the new-machines-only rule above;
- `operator` approval for profiles used outside tightly controlled shares;
- expiry, maximum uses, and one-click **revoke** or **rotate** (rotating
  invalidates the old script);
- a notification for every enrolment, so unexpected machines show up
  straight away.

### 2.4 Agent changes

1. **`install` subcommand.** Reads `AUTODEPLOY_SERVER` and
   `AUTODEPLOY_INSTALL_KEY` from the environment (keeping the key off the
   command line and out of process listings and EDR logs). It then:
   - copies itself to `%ProgramFiles%\AutoDeploy\autodeploy-agent.exe` (the
     same path PXE uses);
   - writes `ServerURL`, enrols with the key and installs and starts the
     service.

   It is idempotent. On a machine that already has the agent, it replaces the
   exe and restarts the service, keeping the identity and secret, so the same
   GPO script can stay assigned. It never logs the key.
2. **`uninstall` subcommand.** Stops and deletes the service and removes the
   files and registry key. It keeps `%ProgramData%\AutoDeploy\identity`
   unless `--purge` is given.
3. **Keep retrying enrolment in service mode.** If the service starts with
   `ServerURL` but no `AgentID` (a GPO startup script on a slow network, or the
   server unreachable), retry `ensureEnrolled` with backoff inside the SCM
   handler instead of exiting. The install key is kept in the protected
   `identity` folder until enrolment succeeds, then deleted.
4. The existing rename → join → software order in `runSelfOnce` needs no
   change.

### 2.5 The bootstrap script and portal

- `GET /api/v1/agent/bootstrap-info?arch=amd64` returns the current agent
  file name and SHA-256 from the downloads dir. This is public, like
  `/agent/download`.
- **Fleet → Agent install** (new page): create, edit, revoke and rotate
  install profiles, and see each profile's uses, last use and enrolled
  machines. For each profile:
  - **Download `Install-AutoDeployAgent.ps1`**, generated with the server URL
    and profile key filled in. It also includes `-Uninstall` support;
  - **Download `install-autodeploy-agent.cmd`**, for GPO startup scripts where
    PowerShell scripts are restricted. It copies the exe from the same
    share and runs `install`;
  - copy-ready instructions for **GPO** (Computer Configuration → Policies →
    Windows Settings → Scripts → Startup, plus "Always wait for the network at
    computer startup"), **Intune** (platform script, or a Win32 app wrapping the
    `.cmd` with detection on `HKLM\SOFTWARE\AutoDeploy\ServerURL`), and **by
    hand**.
- **HTTPS note.** The script checks the exe against a hash from the same
  server. That protects against corrupt downloads but not against someone in
  the middle when the server uses plain HTTP. The page warns when the server
  URL is `http://`. Over HTTP the `.cmd` and share route (exe copied from an
  internal share) is the safer one.
- **Execution policy.** `-ExecutionPolicy Bypass` works unless a GPO enforces
  `AllSigned`. In that case use the `.cmd` route, which needs no script
  signing. This is why the `.cmd` exists, and it avoids needing a certificate.

### 2.6 AD: names, OU and groups without manual steps

1. **Pre-stage the computer object on agent join.** When `/agent/domain-join`
   is about to return credentials and the server's AD integration is
   configured, it first creates the computer object in the effective OU with
   the binding's **AD groups**, the same as `PrepareComputer` on the PXE path.
   `Add-Computer` then joins that object. This fixes the missing groups (gap 1
   from the review).
2. **Name collisions are handled differently from PXE.** `PrepareComputer`
   *deletes and replaces* a same-name object, which is right for a reimage. On
   a stock install the existing object could be a **live, different machine**.
   The agent path therefore:
   - creates the object if it doesn't exist;
   - reuses it if it does exist **and** it is this machine's own previous
     object (the DN recorded on this machine's inventory record);
   - otherwise **refuses**: no join credentials, an `ad_join_status` of
     "name in use", and a notification. *(Open question 3.)*

   This becomes a new `addomain.Service.StageComputer(name, ou, groups,
   ownDN)` beside `PrepareComputer`, tested against `FakeDirectory`.
3. **Without the server's AD integration** (only a join account on the image),
   the join still works and the groups simply aren't applied, as today. The
   machine page says so.
4. **Groups changed later.** When the agent reports a DN and the binding's
   groups differ from what was last applied, the server reconciles them with
   `SetGroupMemberships` (idempotent, audited). Group edits in the portal then
   reach machines installed this way.

### 2.7 "Agent-only" images and boot-menu safety

- New image flag **`agent_only`** ("Use for agent installs only; never offered
  for PXE deploy"). These images hold a loadout and AD join settings with no
  ISO. They are hidden from the boot menu and from "R) Re-image", and the
  reimage actions refuse them with a clear message.
- **Bug fix, regardless of the flag:** the boot menu leaves out any image whose
  ISO doesn't resolve (through inheritance), instead of offering something
  that can't deploy.
- **Docs fix:** `docs/portal/machines.md` describes binding as "deploys
  automatically on next network boot". Correct it to what the code does: the
  bound image is *offered* as Re-image in the menu, and deploys unattended only
  when a remote re-image is queued.

### 2.8 Tests

- Agent:
  - `install` is idempotent (fresh, then re-run over an existing install) and
    keeps the identity;
  - the key is read from the environment, never logged, and kept only until
    enrolment succeeds;
  - `uninstall` with and without `--purge`;
  - enrolment retries in service mode.
- Server:
  - profile key matrix: revoked, expired, max uses, existing machine ignored;
  - `auto` applies the binding before the first identity report (regression
    test for the OEM-name sync);
  - `operator` mode withholds the binding and join credentials until approval;
  - `StageComputer`: create, reuse own object, refuse a foreign object;
    groups applied, and reconciled after edits;
  - the boot menu hides `agent_only` and ISO-less images;
  - audit events never contain the key or the join password.
- Lab: a stock Windows 11 laptop run by hand, by a GPO startup script and by
  Intune. Check for each that it ends up named, in the right OU, in the right
  groups, joined and with its software, with no portal steps for `auto`
  profiles.

---

## 3. Part B — Build driver packages from a device

### 3.1 Workflow

1. A new model arrives. Bootstrap the agent (Part A) onto one unit,
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
install profile and no driver package matches it, capture its drivers
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

Each phase is a separate PR, with docs updated in the same PR
(`downloads.md` manual-install section rewritten, new
`docs/install/agent-bootstrap.md` covering hand, GPO and Intune installs,
`machines.md`, `images.md`, `active-directory.md`, `payloads.md`,
`security.md`, `api.md`).

| # | Phase | Depends on |
|---|---|---|
| 1 | **Quick fixes that help today's manual route:** boot menu hides ISO-less images, `agent_only` image flag, `machines.md` wording, service-mode enrolment retry | — |
| 2 | **AD on agent join:** `StageComputer` (create / reuse own / refuse foreign) with groups, called from `/agent/domain-join`, plus group reconcile on identity report | — |
| 3 | **Install profiles:** table, enrol with `install_key`, `auto` / `operator` modes, notifications, Fleet → Agent install page | 1 |
| 4 | **Bootstrap:** agent `install` / `uninstall` subcommands, `/agent/bootstrap-info`, generated `.ps1` / `.cmd`, GPO and Intune docs | 3 |
| 5 | Driver capture: `driver_package.status/source`, resolver ignores drafts, upload endpoint, `driver_capture` agent job | — (works with any resident agent) |
| 6 | Driver capture UX: machine and bulk action, review / publish / replace, "Models without drivers" | 5 |
| 7 | Optional auto-capture when a new model enrols through a profile | 3, 5 |

Phases 1 and 2 are useful on their own straight away: with them, today's
two-command manual install already gives a correctly grouped AD object.
Phases 1–4 and 5–6 can proceed in parallel.

---

## 5. Open questions for review

1. **Default approval mode** for new profiles: `auto` (fully hands-off, the
   key is the trust) or `operator` (one click per machine in the portal)?
2. **Access PIN for hand installs:** should the script also accept the
   deployment access PIN interactively (no profile key stored in a file),
   for technicians setting up a single machine?
3. **AD name collision:** when the desired name already belongs to another
   computer object, refuse and notify (proposed), or allow a per-profile
   "replace existing object" option for replacement-laptop workflows?
4. **PowerShell:** is it allowed in your estate, or should the `.cmd` and
   share route be the main documented method?
5. **ARM64:** x64 only to start, with ARM64 later?
6. **Driver capture exclusions:** is the proposed default (exclude printers,
   virtual and VPN/filter drivers) right, and should captures always go
   through draft review?
7. **Upload limits:** is 4 GiB the right maximum capture size?
