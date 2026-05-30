# Boot Client

> The Linux pre-OS environment that reads SMBIOS, talks to the
> server, downloads payloads, applies the WIM, injects drivers,
> writes the unattend and reboots.

## Sub-commands

```
autodeploy-boot identify              # log SMBIOS identity and exit
autodeploy-boot --server http://... menu      # interactive menu (default)
autodeploy-boot --server http://... deploy <image-id>
```

Common flags:

| Flag | Meaning |
|---|---|
| `--server URL` | AutoDeploy server base URL. |
| `--sysfs PATH` | DMI sysfs root (default `/sys/class/dmi/id`). |
| `--insecure-tls` | Skip TLS cert verification. **Dev only.** |
| `--disk DEVICE` | Target disk for `deploy` (default `/dev/sda`). |
| `--work DIR` | Scratch directory (default `/run/autodeploy`). |
| `--dry-run` | Log destructive steps without executing them. |
| `--site NAME` | Site name forwarded to the server so payload downloads route to a site-local mirror. |

## Fail-safe behaviour

Every error path in the Boot Client exits **without touching the
disk**:

- No server reachable → no imaging.
- Menu returns no items → no imaging.
- Operator cancels → no imaging.
- Manifest has no WIM → no imaging.
- Hardware identity unreadable → no imaging.

Only the actual `imaging.Apply` pipeline modifies the disk, and
only after the operator has explicitly selected a configuration.

## How a deploy runs

1. iPXE boots the kernel + initramfs.
2. `/init` parses the kernel command line, extracts the server URL,
   execs `autodeploy-boot --server <URL> menu`.
3. The Boot Client reads SMBIOS via `/sys/class/dmi/id/*`, fetches
   the brand for the menu header, prompts for the access PIN if
   one is configured, calls `POST /api/v1/clients/menu` and renders
   the returned list. The operator picks a configuration (or
   chooses Re-image if the machine has a binding).
4. The client POSTs identity to
   `/api/v1/images/{id}/manifest` and downloads every payload
   listed there (WIM/ESD, any matched drivers, the unattend with
   `?uuid=` so per-machine identity is injected). HTTP Range
   requests resume any interrupted fetches.
5. The client partitions the target disk (UEFI GPT: 100 MiB FAT32
   ESP + remainder NTFS), applies the WIM with `wimlib-imagex`,
   **extracts driver zips** into
   `<mount>\Windows\INF\AutoDeploy\<package>\`, writes the unattend
   to `Windows\Panther\unattend.xml`, syncs, unmounts, **ships its
   buffered log events** to `/api/v1/logs/ingest`, and reboots.

Every step is logged with `who`, `what`, `where`, `when`. Sensitive
values never appear in any log.

## The boot image (kernel + initramfs)

The Boot Client ships as a **prebuilt** kernel + initramfs, attached
to each GitHub release and fetched automatically by the installer /
`fetch-ipxe.sh` into `$AUTODEPLOY_DATA_DIR/ipxe/` as
`autodeploy-kernel` and `autodeploy-initrd`. Operators don't build
anything.

The initramfs bundles the statically-linked `autodeploy-boot` binary,
the kernel's modules (loaded at boot to bring up the NIC and disks),
and the host tools the imaging plan calls out to:

| Tool | Provides |
|---|---|
| `sgdisk` (gptfdisk) | Partitioning |
| `mkfs.fat` (dosfstools) | ESP filesystem |
| `mkfs.ntfs` (ntfs-3g) | Windows partition filesystem |
| `wimlib-imagex` (wimlib) | WIM/ESD apply |
| `modprobe`/`depmod` (kmod) | Load NIC + disk drivers |
| `mount`, `umount`, `cp`, `mkdir`, `reboot`, `sync` | busybox / coreutils |

Building your own is only needed for exotic hardware. The reference
script is `scripts/initramfs/build-initramfs.sh` (run in CI by
`.github/workflows/build-bootimage.yml` to produce the release
asset):

```sh
./scripts/initramfs/build-initramfs.sh
# Writes build/initrd.img — copy to $AUTODEPLOY_DATA_DIR/ipxe/autodeploy-initrd.
# Ships the running kernel's modules; pair with that kernel's vmlinuz as
# $AUTODEPLOY_DATA_DIR/ipxe/autodeploy-kernel.
```

## Delivery via iPXE

The Boot Client is delivered via iPXE chainloaded over HTTP.
AutoDeploy serves an iPXE script that points iPXE at the kernel +
initramfs:

```
GET /ipxe/boot.ipxe
```

The standard bootstrap is:

```
firmware PXE  →  TFTP (iPXE bootstrap)  →  iPXE  →  HTTP (boot.ipxe)  →  AutoDeploy
```

iPXE is the small bridge between TFTP-only firmware and
AutoDeploy's HTTP-only flow. The full DHCP and firmware story is
in [pxe-setup.md](pxe-setup.md).

For an environment that already has iPXE-aware firmware (or a DHCP
that already chainloads iPXE), point its bootfile at
`http://<autodeploy>/ipxe/boot.ipxe` and you're done.

Put your kernel and initramfs in `$AUTODEPLOY_DATA_DIR/ipxe/`:

```
$AUTODEPLOY_DATA_DIR/ipxe/
├── undionly.kpxe        # iPXE bootstrap, BIOS
├── ipxe.efi             # iPXE bootstrap, UEFI x64
├── snponly.efi          # iPXE bootstrap, UEFI x64 (SNP-driver flavour)
├── ipxe.pxe             # alt BIOS bootstrap
├── ipxe-arm64.efi       # iPXE bootstrap, UEFI arm64
├── autodeploy-kernel    # your vmlinuz
└── autodeploy-initrd    # initramfs from build-initramfs.sh
```

The install scripts fetch the five iPXE bootstrap binaries
automatically; you only have to supply the kernel + initramfs.

## Log shipping

The Boot Client buffers its slog events in memory (bounded — 2048
events, oldest-drop) and POSTs the drain to
`/api/v1/logs/ingest` before reboot. The portal's [Logs
page](logging.md) shows them next to the server's own events.

## Site routing

When the Boot Client passes the `--site NAME` flag, or when the
DHCP configuration has set `autodeploy.site=<name>` on the kernel
cmdline, the manifest endpoint rewrites WIM/driver/software URLs
to the highest-priority healthy mirror for that site. The unattend
stays on the primary. See [Scaling → Payload mirrors](scaling.md#payload-mirrors).
