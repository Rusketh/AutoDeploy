# Deploy Spine Realignment: Boot-the-Media, not Capture/Apply

Status: PROPOSED — implementation in progress on the partition path.

## Why

The Boot Client today (`boot-client/internal/imaging/imaging.go`) deploys
Windows by `wimlib-imagex apply` of the `install.wim` extracted from the
ISO, then injects drivers and drops the unattend into the applied image.
It never makes the disk bootable (the ESP is formatted but never
populated) — so a deployed disk does not UEFI-boot.

More importantly, **capture/apply is the wrong model for AutoDeploy.** It
is Windows-specific (it understands WIMs and would need to hand-build a
BCD), and it diverges from the intended design: AutoDeploy should stage a
*bootable copy of the install media* and let the media's own installer
run. That is OS-agnostic — it boots any bootable ISO, which is what the
roadmap's future Linux support needs.

Note: the written design docs themselves currently describe the
capture/apply model (`AutoDeploy_Project_Context.txt` §2 steps 7–8;
`roadmap.txt` Phase 5). Those sections are corrected as part of this work
so future contributions don't drift back.

## The intended flow

1. Operator selects an image.
2. The Boot Client creates an on-disk **boot partition** on the target:
   shrink an existing partition to make room; if it cannot shrink, erase
   the disk (the operator is warned, in the portal and at the boot menu,
   that a failed deploy may leave the previous OS removed).
3. The Boot Client downloads the **full extracted ISO contents** onto the
   boot partition (not just the WIM).
4. It adds the answer file (`autounattend.xml`) and any driver packages
   onto the boot partition.
5. It makes the partition UEFI-bootable and reboots into it.
6. **The media's own setup loads** from the partition (Windows Setup;
   later, a Linux installer).
7. Setup runs the unattend if one is present.

The Boot Client never runs `wimlib apply` and never hand-builds a BCD.
Control is transferred to the media's installer.

(RAM-disk boot was considered and dropped: a running Linux kernel cannot
hand off to Windows in RAM — that is an iPXE/`wimboot` firmware trick, a
separate stage entirely. Scope here is the on-disk boot partition only.)

## Hard constraints

### FAT32 4 GiB file limit
UEFI firmware boots from FAT32, but a modern `sources\install.wim` /
`install.esd` is frequently larger than FAT32's 4 GiB per-file limit, so
the media cannot live on a single FAT32 volume. The Rufus-proven layout:

- A small **FAT32 ESP** carrying only the bootloader
  (`\EFI\BOOT\bootx64.efi` = the media's `bootmgfw.efi`) and `\boot\`.
- A larger **NTFS/exFAT partition** carrying `sources\` (the big WIM) and
  the rest of the media.
- The EFI bootloader and Windows Setup both read across to the second
  partition by volume label, exactly as Windows install media does.

The Boot Client's initramfs already bundles `mkfs.fat` and `mkfs.ntfs`.

### Shrink-or-erase
Making room on a target that already has an OS:
- Attempt a non-destructive shrink of the last/largest existing
  partition (NTFS via `ntfsresize`, ext via `resize2fs`) to free a few
  GiB at the end.
- If no partition can be shrunk safely, fall back to wiping the disk
  (`sgdisk --zap-all`) and using the whole disk — destructive.
- Either way the operator was warned before the deploy started.

## Component changes

| Area | Change |
|---|---|
| `boot-client/internal/imaging` | Replace capture/apply with **media staging**: build boot partition(s), copy the downloaded media tree, drop `autounattend.xml` at the media root + drivers under `$WinPEDriver$\`, set the EFI boot entry (`efibootmgr`), reboot. |
| `boot-client` main | Download the **media tree** (manifest `iso-media` items) instead of a single `iso-wim`. |
| `server/internal/payload/manifest.go` | Emit the extracted media as a tree the client can mirror — either a manifest listing every file under `iso/{id}/files/` (via `BlobStore.ListDir`) or a single base + recursive fetch. New role `iso-media`. Keep `iso-wim` until the client switches. |
| `server/internal/payload/serve.go` | Already serves `/payload/iso/{id}/{path...}` file-by-file — reused. Add a media index endpoint if the client mirrors by manifest. |
| docs | Rewrite `AutoDeploy_Project_Context.txt` §2 steps 7–8 and `roadmap.txt` Phase 5 to the boot-the-media model. |

## Sequencing

1. **This plan doc** (here) — agree the model.
2. **Server: media index + `iso-media` manifest role.** Additive; does not
   break the existing `iso-wim` path.
3. **Boot Client: media-staging imaging** (the on-disk partition path),
   replacing capture/apply. New focused tests against the Recorder for the
   exact command sequence (partition → mkfs → copy media → autounattend →
   drivers → efibootmgr → reboot).
4. **Docs rewrite.**
5. **Validate on the Hyper-V rig.**

The on-disk partition path is fully achievable end-to-end with tools the
initramfs already carries (sgdisk, mkfs.fat, mkfs.ntfs, cp) plus
`efibootmgr` + `ntfsresize` (to be added to the initramfs tool list).
