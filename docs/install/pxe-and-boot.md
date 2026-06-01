# PXE & boot setup

To deploy a machine, AutoDeploy needs to take control of it the moment it powers on — before any
operating system loads. It does this with **network booting**: the machine asks the network for
something to boot, your DHCP server points it at AutoDeploy, and AutoDeploy hands it a small Linux
environment that runs the [Boot Client](../introduction.md#boot-client-autodeploy-boot).

This page explains how that chain works and how to configure DHCP for both classic (legacy BIOS)
PXE and modern UEFI network boot. You only set this up once.

## How the boot chain works

AutoDeploy uses **iPXE** — a flexible open-source network bootloader — as the bridge between a
machine's firmware and the Boot Client. The chain has four links:

1. **Firmware → iPXE.** The machine's built-in PXE ROM does a DHCP request and downloads an iPXE
   binary (over TFTP for classic PXE, or over TFTP/HTTP for UEFI). Your DHCP server decides which
   file to hand out and which server to fetch it from.
2. **iPXE → bootstrap script.** Once iPXE is running, it automatically fetches an
   `autoexec.ipxe` bootstrap script. AutoDeploy **generates this script for you** — you never have
   to build a custom iPXE binary with a script baked in. The stock iPXE binaries work as-is.
3. **Bootstrap → boot image.** The bootstrap chainloads `http://<your-server>/ipxe/boot.ipxe`,
   which loads the **boot image**: a Linux kernel (`autodeploy-kernel`) and an initramfs
   (`autodeploy-initrd`) that contains the Boot Client.
4. **Boot image → Boot Client.** The kernel boots, the Boot Client starts, reads the machine's
   SMBIOS identity, and contacts the server's API. From here the machine appears in
   [Machines](../portal/machines.md) and either shows a boot menu or auto-deploys a bound image.

```
firmware PXE/HTTP boot
  → DHCP hands out a stock iPXE binary (+ AutoDeploy as next-server)
  → iPXE auto-fetches autoexec.ipxe   (TFTP or HTTP, from AutoDeploy)
  → autoexec.ipxe chainloads http://<autodeploy>/ipxe/boot.ipxe
  → boot.ipxe loads autodeploy-kernel + autodeploy-initrd
  → the initramfs runs autodeploy-boot, which calls back to /api/v1/...
```

Because the bootstrap is served dynamically, the same generic iPXE binary works whether a machine
boots over TFTP or UEFI HTTP — the server fills in the rest.

## What the server provides

The Linux installer ([Installing the AutoDeploy server](linux-server.md)) sets most of this up:

- **iPXE bootstrap, served two ways.** The server answers `GET /autoexec.ipxe` over HTTP, and the
  built-in TFTP server synthesises the same `autoexec.ipxe` on the fly for TFTP-booted clients.
  Neither requires a file on disk.
- **The chainload script**, `GET /ipxe/boot.ipxe`, which loads the boot image.
- **A built-in TFTP server** so you don't have to run a separate TFTP daemon (see below).
- **Static boot files** under the iPXE directory: the iPXE binaries and the boot image. The
  installer's `fetch-ipxe.sh` helper downloads these.

You can confirm everything is in place from the portal at
**[Settings → PXE](../portal/settings.md#pxe)**, which shows whether each boot file is present and
the exact bootstrap URLs.

### The built-in TFTP server

AutoDeploy includes a small read-only TFTP server so classic PXE works end-to-end without a
separate daemon. It is controlled by the `AUTODEPLOY_TFTP_ADDR` setting in
`/etc/default/autodeploy` and defaults to `:69` (the standard TFTP port). Set it empty to disable
the listener if you already run your own TFTP server.

The TFTP server:

- serves the iPXE binaries and boot image from the iPXE directory,
- synthesises `autoexec.ipxe` when a client requests it (a real file of the same name on disk
  always takes precedence, so you can drop a custom one if you ever need to),
- is read-only — it refuses uploads.

The synthesised TFTP bootstrap resolves the AutoDeploy server from DHCP option 66
(`next-server`), so a stock iPXE binary that booted via classic PXE never needs a server URL baked
in.

### The iPXE binaries

`fetch-ipxe.sh` downloads stock iPXE binaries into the iPXE directory. The ones you hand out via
DHCP depend on the client's firmware:

| File | Firmware / use |
|------|----------------|
| `undionly.kpxe` | Legacy BIOS PXE (chainloaded by the NIC's PXE ROM) |
| `ipxe.pxe` | Legacy BIOS (alternative NBP, a fallback for flaky NICs) |
| `ipxe.efi` | UEFI PXE / UEFI HTTP Boot |
| `snponly.efi` | UEFI PXE via the firmware's SNP network driver (a fallback for Hyper-V Gen2 and some NICs) |
| `ipxe-arm64.efi` | UEFI PXE on ARM64 hardware |

These are unmodified, generic iPXE builds. The script fetches from the AutoDeploy release first
and falls back to `boot.ipxe.org`; either works the same way, because the script is served by the
server, not embedded.

### The boot image

The boot image is two release-only files placed alongside the iPXE binaries:

| File | Purpose |
|------|---------|
| `autodeploy-kernel` | The Boot Client's Linux kernel |
| `autodeploy-initrd` | The initramfs containing `autodeploy-boot` |

If these are missing, iPXE will chainload successfully but have nothing to boot. You can build your
own with `scripts/initramfs/build-initramfs.sh` if you are not using a release that ships them.

## Configuring DHCP

Network boot needs two things from DHCP:

1. **`next-server`** (option 66) pointing at the AutoDeploy host — this is where clients fetch the
   iPXE binary and where the TFTP bootstrap resolves the server.
2. **A boot filename** telling the firmware which iPXE binary to load.

The guidance below is vendor-neutral; the option names map onto any DHCP server (ISC `dhcpd`,
`dnsmasq`, Windows DHCP, your router/firewall, etc.).

> **Shortcut:** the portal's [Settings → PXE](../portal/settings.md#pxe) page generates ready-made,
> copy-paste DHCP snippets for UniFi, dnsmasq, ISC dhcpd, Microsoft DHCP, and OPNsense/pfSense,
> each pre-filled with your server's IP. If you run one of those, start there.

### Legacy BIOS PXE

For BIOS clients, point `next-server` at AutoDeploy and set the boot filename to `undionly.kpxe`:

- **next-server / option 66:** the AutoDeploy host's IP address
- **boot filename / option 67:** `undionly.kpxe`

The firmware downloads `undionly.kpxe` over TFTP, iPXE starts, fetches the synthesised
`autoexec.ipxe`, and chainloads the boot image.

### UEFI network boot

UEFI clients need an EFI iPXE binary. Point `next-server` at AutoDeploy and set the boot filename
to `ipxe.efi` (or `snponly.efi` if your firmware's network stack needs the SNP build, or
`ipxe-arm64.efi` on ARM64):

- **next-server / option 66:** the AutoDeploy host's IP address
- **boot filename / option 67:** `ipxe.efi`

**UEFI HTTP Boot.** Some UEFI firmware can boot directly over HTTP instead of TFTP. In that case,
hand the client an HTTP URL to the iPXE binary (for example
`http://<your-server>/ipxe/static/ipxe.efi` if you serve it over HTTP, or your own HTTP boot
source). Once iPXE is running it fetches `autoexec.ipxe` over HTTP from the server it reached, so
the rest of the chain is identical.

### Serving BIOS and UEFI from one DHCP scope

A mixed fleet has both BIOS and UEFI machines on the same network. DHCP servers can pick the boot
filename based on the client's reported architecture (the DHCP "client architecture" option), so
BIOS clients receive `undionly.kpxe` and UEFI clients receive `ipxe.efi` from the same scope.
Consult your DHCP server's documentation for its conditional/class syntax; the values to hand out
are the filenames in the tables above.

## Verifying the chain

After configuring DHCP:

1. Open **[Settings → PXE](../portal/settings.md#pxe)** and confirm every boot file shows
   **present** and the bootstrap URLs look right.
2. Network-boot a test machine (a VM is fine). You should see iPXE start, print
   `=== AutoDeploy bootstrap ===`, chainload the boot image, and then the Boot Client's menu.
3. The machine should appear in **[Machines](../portal/machines.md)** within a few seconds of
   contacting the server.

If a booting machine drops to an `iPXE>` shell instead, the chain could not reach the server. From
that prompt you can test the chainload manually:

```
chain http://YOUR-SERVER-IP/ipxe/boot.ipxe
```

The most common cause is `next-server` (option 66) not being set or not pointing at the AutoDeploy
host.

## Fail-safe behaviour

The Boot Client never harms a working machine. If it cannot reach the server, finds no deployable
image, or hits any error before it has begun writing to disk, it exits cleanly and the firmware
boots the machine's existing operating system. Network booting a machine to check it in is always
safe.

## Next steps

- Gate who can start a deployment from the boot menu with an
  [Access PIN](../portal/settings.md#access-pin).
- Upload Windows media and build your first [image](../portal/images.md).
- For large or remote fleets, serve payloads from a site-local [mirror](../portal/mirrors.md).
