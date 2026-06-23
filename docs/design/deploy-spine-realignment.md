# Deploy Spine Realignment — Implementation Plan (SWM / boot-the-media)

Status: IMPLEMENTED (Parts 1–3 shipped — server-side prepare/split, portal
status surfacing, and the `iso-media` Boot Client media-staging rewrite). Parts
4–5 (unattend single-disk coexistence + this doc/roadmap rewrite) remain.
Supersedes the layout sketch previously in this file.

## Decision

Deploy by **staging a bootable copy of the install media** onto the
target and letting the media's own installer run — not `wimlib apply`.

The FAT32 4 GiB limit is solved by **splitting an oversized
`sources/install.wim` (or `.esd`) into `<4 GiB` `install.swm` parts**,
which Windows Setup reads natively. This means:

- **One plain FAT32 partition** for the whole media — no NTFS, no GRUB,
  no UEFI:NTFS shim, no third-party EFI binaries.
- The split runs **once, server-side, at ISO ingest** — the operator
  configures nothing and downloads nothing extra (`wimlib` is already
  bundled server-side).
- The **Boot Client stays generic**: make a FAT32 partition, copy the
  media tree, boot `\EFI\BOOT\BOOTX64.EFI`. The same path will boot a
  Linux ISO later; the only Windows-specific step (the WIM split) lives
  in server ingest, off the generic boot path.

Rejected alternatives and why: all-NTFS + GRUB (chainload OOM risk,
per-OS grub.cfg); all-NTFS + UEFI:NTFS shim (ships niche GPLv3 EFI
binaries); FAT32-boot + NTFS-`install.wim` (Setup does not reliably find
`install.wim` across volumes). See git history of this file.

---

## Part 1 — Server: split oversized install image at ingest

**Where.** `server/internal/payload/serve.go`, in the extract path that
already runs (upload auto-extracts; `extractISO` calls `ExtractISO`).
After extraction succeeds, run a new prepare step over the extracted
`files/sources/` directory.

**New function** (`payload` package, e.g. `media_prep.go`):

```
PrepareBootMedia(filesDir string) (MediaPrep, error)
```

- Look for `sources/install.wim`, else `sources/install.esd`.
- If it is `>= 4000 MiB` (safe margin under FAT32's 4 GiB-1):
  - `wimlib-imagex split <img> sources/install.swm 3800`
    → produces `install.swm`, `install2.swm`, …
  - Remove the original `install.wim`/`.esd` (Setup must see *only* the
    `.swm` set, or it may prefer the oversized original).
- Idempotent: if `install.swm` already exists and the original is gone,
  it's already prepared — skip.
- Returns: format (`wim`/`esd`/`swm`), original byte size, part count,
  and whether a UEFI bootloader (`efi/boot/bootx64.efi`) is present
  (surfaced as a portal warning if missing).

**Threshold/chunk constants** live next to `fat32MaxFileBytes`.

**Failure handling.** A split failure does not fail the upload; it is
recorded on the ISO row (`PrepError`) and surfaced in the portal so the
operator can re-prepare. The ISO is simply "not deploy-ready".

---

## Part 2 — ISO model + persistence

Extend `model.ISO` (`server/internal/model/types.go`) with prep status so
the portal can report it (no operator-set fields — purely descriptive):

```
InstallImageFormat string  // "wim" | "esd" | "swm" | ""  (pre-extract)
InstallImageBytes  int64   // size of the install image before any split
SWMParts           int     // 0 = not split (fit FAT32); N = split into N
BootloaderPresent  bool    // efi/boot/bootx64.efi found in the media
PrepError          string  // non-empty => not deploy-ready
MediaPreparedAt    *time.Time
```

- DB migration adds the columns (follow the existing migration pattern in
  `internal/model`).
- `ISORepo.Update` already persists the row; the extract handler sets
  these fields from `PrepareBootMedia` and calls `Update`.

**Deploy-readiness** becomes a derived predicate:
`PrepError == "" && BootloaderPresent && extracted`.

---

## Part 3 — Manifest: `iso-wim` → `iso-media`

`server/internal/payload/manifest.go`: replace the single `iso-wim` item
with an **`iso-media`** item that hands the Boot Client the whole media
tree, using the index endpoint already built:

```
ManifestItem{
  Role: "iso-media",
  URL:  "{base}/payload/iso/{id}/index.json",   // full file list
  Base: "{base}/payload/iso/{id}/",             // join with each rel path
  OS, Name, Size(total),
}
```

- Keep emitting only when the ISO is extracted **and** deploy-ready.
- `iso-wim` is removed once the Boot Client no longer consumes it (same
  PR as the Boot Client rewrite, to avoid a dangling role).

---

## Part 4 — Boot Client: media staging (replaces capture/apply)

Rewrite `boot-client/internal/imaging`:

1. **Download** the media tree: fetch `index.json`, then mirror every
   listed file under a local staging dir (reuse the existing mirror/HTTP
   fetch with the same retry/backoff the client already uses).
2. **Partition** the target: GPT, a **single FAT32 partition** typed as
   ESP, sized = media total + margin (it does *not* need the whole disk —
   see single-disk note). Replace the now-wrong `bootmedia.go` two-part
   primitive with this single-FAT32 layout (keep the tested `partName`
   nvme handling and the Recorder-based command-sequence tests).
3. **Copy** the media tree onto the partition.
4. **Place** `autounattend.xml` at the partition root (Setup auto-detects
   it there) and driver packages under `$WinPEDriver$\<pkg>\` (WinPE
   auto-loads that folder).
5. **Register boot**: `efibootmgr` entry → the FAT32 partition's
   `\EFI\BOOT\BOOTX64.EFI`, set `BootNext`; rely on the EFI fallback path
   as backstop. `sync`, reboot.

The `wimlib apply` / driver-injection-into-applied-image / ESP-format
code is deleted.

**Initramfs tools:** add `efibootmgr`. `mkfs.fat`, `sgdisk` already
present. `wimlib` is no longer needed on the client (split is
server-side) and can be dropped from the client image.

---

## Part 5 — The single-disk coexistence problem (cross-cutting)

The media now lives on the **same disk** Windows installs to. If the
answer file wipes disk 0, it destroys its own boot media mid-install.
This is independent of SWM and must be handled in the **unattend
generator**, which must agree with the Boot Client's disk layout:

- Boot Client creates the FAT32 media partition; the generated
  `<DiskConfiguration>` installs Windows into the **remaining free
  space**, explicitly **not** wiping the media partition.
- **Post-install cleanup**: the agent's first-logon step deletes the
  media partition and extends `C:` (the media partition is transient
  scaffolding).
- The generator therefore needs to know the layout the Boot Client
  produced — captured as a small shared contract (partition index /
  label) rather than duplicated constants.

This is the highest-risk item end-to-end and gets its own validation pass
on the rig.

---

## Part 6 — Portal handling

No new operator-set options (the SWM split is automatic). The portal's
job is to **report boot-media readiness** transparently.

**ISO lifecycle shown to the operator:**

```
Created → Uploaded → Extracted → Media prepared → Deploy-ready
                                   (split if >4 GiB)
```

- **`iso_list.html`** — add a **Status** column rendering a badge from
  the derived state: `Uploading…` / `Extracted` / `Preparing…` /
  `Ready` / `Needs attention` (when `PrepError` or no bootloader).
- **ISO detail/edit (`iso_form.html`, `iso.go`)** — a **Boot media**
  panel showing:
  - install image format + original size,
  - “Split into N parts for FAT32 boot media” (or “Fits FAT32 — no split
    needed”),
  - “UEFI-bootable: yes/no” (from `BootloaderPresent`),
  - any `PrepError`.
- **Action**: a **Re-prepare media** button (re-runs extract+split) for
  recovery, mirroring the existing `/portal/isos/{id}/extract` route.
- **Deploy guards**: surfaces (image edit, deploy menu) that reference an
  ISO show a clear "not deploy-ready" note when the derived predicate is
  false, so an un-prepared ISO can't silently be selected.

---

## Part 7 — Sequencing (small, reviewable PRs)

1. ✅ **Server prepare step + model fields + migration** (`PrepareBootMedia`,
   ISO columns, wired into extract). Tested with a fake oversized WIM.
2. ✅ **Portal status surfacing** (list badge, detail panel, re-prepare,
   deploy guard). Server-only behaviour already in place from (1).
3. ✅ **Manifest `iso-media`** + Boot Client media-staging rewrite +
   initramfs `efibootmgr`; removed `iso-wim` and capture/apply. Recorder
   tests for the new command sequence.
   - Boot Client: `StageMedia` builds a single FAT32 boot partition at the
     END of the disk (front left free for Setup), copies the mirrored
     media, drops `autounattend.xml` at the root + drivers under
     `$WinPEDriver$\`, registers the partition with `efibootmgr`.
   - `main.go` mirrors the media tree via the `iso-media` index.
   - initramfs: added `efibootmgr` + `unzip`, mounts `efivarfs`; dropped
     `mkfs.ntfs`/`wimlib` (no NTFS, split is server-side).
4. **Unattend coexistence** (DiskConfiguration + agent cleanup) — paired
   with the first real rig boot. NOTE: `StageMedia` places the boot
   partition at the disk END; the answer file must install Windows into
   the free space at the FRONT without wiping it, and the agent deletes
   the boot partition + extends C: post-install.
5. **Docs**: rewrite Project-Context §2 and roadmap Phase 5 to this model.

## Testing

- Server: unit test `PrepareBootMedia` with a synthetic >4 GiB file
  (sparse) to assert split+remove+idempotency and status fields.
- Manifest: assert `iso-media` URL/Base and deploy-ready gating.
- Boot Client: Recorder asserts partition→format→copy→autounattend→
  drivers→efibootmgr→reboot, incl. nvme part naming.
- Portal: render tests for each badge state.
- End-to-end: Hyper-V rig — the only place the boot chain and the
  single-disk coexistence can be truly validated.

## Open risks

- **Single-disk coexistence** (Part 5) — most likely to need iteration.
- **`.esd` splitting** — confirm `wimlib-imagex split` handles solid ESD
  the same way; if not, convert/handle separately.
- **efibootmgr vs fallback path** — some firmware ignores added NVRAM
  entries; the `\EFI\BOOT\BOOTX64.EFI` fallback is the backstop.
