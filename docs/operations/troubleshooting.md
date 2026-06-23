# Troubleshooting

This page lists common symptoms and where to look. The [audit log](../portal/logs.md) is your
first stop for almost everything — all components ship structured events to the server.

## The server won't start

- **Check the logs:** `journalctl -u autodeploy -f`.

> **HTTPS without a real certificate in production isn't a startup failure.** With
> `AUTODEPLOY_DEV=false`, enabling `AUTODEPLOY_HTTPS_ADDR` without `AUTODEPLOY_TLS_CERT` and
> `AUTODEPLOY_TLS_KEY` still starts: the server auto-generates a self-signed certificate under
> `<data-dir>/tls/` and logs a warning. Browsers and clients will fail TLS verification against
> that cert — supply a real `AUTODEPLOY_TLS_CERT`/`AUTODEPLOY_TLS_KEY` for a trusted certificate.

See the [configuration reference](../reference/configuration.md) for valid values.

## I can't log in / lost the admin password

On first start the admin password is written to `/var/lib/autodeploy/admin-bootstrap.txt`. If you
already deleted it and forgot the password, another operator account can reset it under
[Settings → Accounts](../portal/settings.md#accounts).

## An ISO won't extract or prepare

Modern Windows ISOs are UDF and need an extraction tool, and a large `install.wim` may need
splitting:

- Install **p7zip** (`7z`/`7za`) or `bsdtar` for extraction.
- Install **wimlib** (`wimlib-imagex`, packaged as `wimtools`/`wimlib-utils`) for splitting large
  `install.wim` files.

The Linux installer attempts to install these automatically; if it couldn't (air-gapped or an
unknown distro), install them by hand. The ISO's page in the portal shows a clear message when one
is missing. See [Payloads → ISOs](../portal/payloads.md#isos).

## A machine PXE-boots but doesn't reach AutoDeploy

- Verify DHCP points network clients at AutoDeploy's iPXE bootstrap and that the iPXE binaries are
  present under `<data-dir>/ipxe/` (re-run `scripts/fetch-ipxe.sh` if needed).
- Confirm the boot image (kernel + initramfs) is in place.
- **UEFI Secure Boot:** if the firmware refuses the boot file (Secure Boot violation / "Access
  Denied"), hand out `ipxe-shim.efi` instead of `ipxe.efi` — the shim is Microsoft-signed. If iPXE
  loads but the **kernel** stage then fails under Secure Boot, confirm `autodeploy-shim.efi` is
  present in the iPXE directory (re-run `scripts/fetch-ipxe.sh`) — `boot.ipxe` needs it to verify
  the kernel. A brief `Security Policy Violation` message before the kernel boots is cosmetic. If a
  specific machine still fails to bring up its NIC/disk, its driver may be an unsigned/out-of-tree
  module that kernel lockdown blocks under Secure Boot — boot that machine with Secure Boot off.
  See [UEFI Secure Boot](../install/pxe-and-boot.md#uefi-secure-boot).
- See [PXE & boot setup](../install/pxe-and-boot.md).

## A booting machine doesn't show the deploy menu

If an [access PIN](../portal/settings.md#access-pin) is set, the boot client must submit a valid
PIN before it can deploy. Check the PIN under Settings → Access PIN.

## A deploy stalls partway through, and no logs reach the portal

A deploy that gets past the image-selection screen but then stops — a frozen
progress bar, nothing on the [logs page](../portal/logs.md) — is usually a
**network adapter that stops carrying the payload download**. USB Ethernet
adapters (the Realtek RTL8153 / r8152 in most Dell USB-C and USB 3.0 PXE
dongles) are the common culprit: they pull the kernel and initramfs fine over
iPXE, then wedge once the multi-GB media transfer starts. Because the link is
down, the client can't ship its logs, so the portal stays empty.

To see what's happening, **uncheck "Show imaging progress"** on the
image-selection screen before you deploy (it's ticked by default; toggle it
with the mouse or the **space** bar). With it unchecked, the boot client hands
the screen back to the text console for the deploy instead of drawing the
progress bar, and re-enables kernel console messages (the boot environment
silences them so they don't bleed through the graphical UI). Both then print
live on the machine — the kernel's own USB-reset / disk-error lines alongside
the boot client's diagnostics: the bound NIC driver, USB link speed, error
counters, and each download retry. That's exactly the detail you need to
diagnose a stalled USB NIC when nothing reaches the portal.

The boot-package version shown in the corner of every boot screen tells you
which release is running — confirm it's the one you expect before digging
further. See also [PXE & boot setup](../install/pxe-and-boot.md).

## A driver download stalls with "short read" (`have X of Y bytes`)

If the stalled file is a **driver** payload and the error is a short read —

```
download /run/autodeploy/payload-driver-… stalled for 5m0s: short read: have 610747486 of 1654623973 bytes
```

— where the server is reachable and actually served the smaller number (`X`),
the recorded download size (`Y`) didn't match the bytes the server serves for
that package. The Boot Client downloads the **uploaded driver zip**, but an
older bug recorded a driver package's `SizeBytes` as the *uncompressed*
extract total (typically 2–3× larger) when you clicked **Extract** on it. The
client then waited for bytes that don't exist and stalled out after its retry
budget.

The server now reports each driver payload's true on-disk size in the manifest,
so newly built manifests heal themselves with no action needed. If you are on a
build from before the fix, correct an affected package by re-uploading its zip
(or clicking **Extract** again) on its **Drivers** page — that re-records the
served size.

## A deploy fails at partitioning (`zap … exit status 2`, wrong disk)

A deploy that reaches the disk step and then fails with something like
`deploy.partition.fail … zap /dev/sda exit status 2` means the partitioner
(`sgdisk`) couldn't open the target disk — almost always because **that disk
device doesn't exist** on this machine. The classic case is a machine whose
only drive is **NVMe** (`/dev/nvme0n1`), not the older SATA `/dev/sda`. This
correlates strongly with USB-NIC machines: the modern laptops and small-form
PCs that lack built-in Ethernet are the same ones that ship NVMe storage.

The boot client now **auto-detects the internal fixed disk** when no disk is
forced, preferring NVMe then SATA/SCSI and skipping removable and USB-attached
disks (so it never images a USB stick). The console log shows what it found:

```
deploy.disk.detect   candidates=[/dev/nvme0n1]
deploy.disk.auto     disk=/dev/nvme0n1
```

If detection picks the wrong disk (multiple internal disks) or finds none, force
the device explicitly with **`autodeploy.disk=<device>`** on the kernel command
line (set it in your iPXE boot script alongside `autodeploy.server=`), or pass
`-disk <device>` when running the client directly. A disk you name explicitly
that turns out to be absent fails safe — the client won't guess and wipe a
different one. To see the detection log live, untick **"Show imaging progress"**
on the image-selection screen (see the stalled-deploy section above).

## "No usable target disk found" (the kernel sees no disk)

If detection reports **`no usable target disk found`** and the log shows an
**empty** block-device list:

```
deploy.disk.detect   candidates=[]
deploy.disk.none     block_devices=[]
```

then the kernel itself can't see any internal disk — there's nothing for the
client to pick. The cause is a storage driver the boot image isn't loading, or
a firmware storage mode. Two common cases:

**eMMC storage (small / education machines).** Devices like the **Dell Latitude
3190** (Intel Gemini Lake) boot from soldered eMMC, which appears as
`/dev/mmcblk0` — but only when the SDHCI host-controller driver
(`sdhci-acpi`) is loaded. Current boot images load the MMC/SDHCI module set, so
the eMMC enumerates and is detected automatically. If you're on an older boot
image, rebuild it from `scripts/initramfs/build-initramfs.sh` (or update to the
latest release).

**NVMe behind RAID / Intel VMD (modern Intel machines).** With **RAID / Intel
RST ("RAID On")** or **Intel VMD** enabled in firmware, the NVMe drive is hidden
behind the VMD controller and the plain `nvme` driver finds nothing. Two fixes:

- **Load the VMD driver (preferred, no per-machine change).** Current boot
  images load the `vmd` module before `nvme`, so VMD-hidden NVMe drives
  enumerate normally. Older images need rebuilding/updating as above.
- **Switch the firmware to AHCI.** In the machine's BIOS/UEFI, set the
  SATA/storage mode from *RAID*/*RST* to **AHCI** (or disable *VMD*). The disk
  then appears to the standard driver. (On Windows-preinstalled machines, note
  that flipping to AHCI can stop an *existing* Windows install from booting —
  not a concern when you're about to re-image.)

Either way, the boot image must ship the right kernel module. The image bundles
the whole module tree, so a current `autodeploy-initrd` already has them — the
fix for an old image is simply to rebuild or update it.

If instead the block-device list is **non-empty** but the disk you expect was
filtered out (e.g. it's marked removable, or sits on USB), force it with
`autodeploy.disk=<device>` — and tell us what `block_devices` listed, since that
points at a detection gap to fix. Untick **"Show imaging progress"** to watch
this on the console live, or read it from the [logs page](../portal/logs.md)
(the listing is shipped before the client exits).

## A machine doesn't appear in inventory

Machines appear automatically the first time they network-boot or an agent checks in (they are
identified by SMBIOS UUID). If a machine is missing, confirm it actually reached the server (check
the [logs](../portal/logs.md)) and that its firmware exposes a stable SMBIOS UUID.

## Software installed when it shouldn't have (or didn't)

A package runs its [install steps](../reference/install-steps.md) only when its
[detection rules](../reference/detection-rules.md) report it as *not* installed. If a package keeps
reinstalling, its detection rule probably isn't matching the installed state; if it never installs,
the detection rule may be matching too eagerly. Review the package's rules under
[Software](../portal/software.md).

## A machine doesn't join the domain

Prefer **agent-driven join**, configured on the image
([Images → Active Directory domain join](../portal/images.md#active-directory-domain-join)). The
agent joins after first boot, when networking and DNS are up, and retries on its next check-in if
the directory was briefly unreachable.

If you're using the **legacy unattend join** and Setup stalls for a long time during the *getting
ready* / specialize phase and then comes up unjoined, the machine could not reach a domain
controller mid-Setup — almost always because DNS during Setup doesn't point at the AD DNS server,
so the domain can't be resolved to a DC. Switch the image to agent-driven join, or ensure the
imaging network's DNS resolves the domain. Either way, confirm the **join account** can add
computers to the target OU.

After a successful join the agent reports the machine's new computer name and AD location, which
appear on the [machine's page](../portal/machines.md).

## Still stuck?

Search the [audit log](../portal/logs.md) filtered by the affected machine, component, and time
range to see exactly what each component reported.
</content>
