# Boot Client and PXE setup

> **Status.** Phase 3. The Boot Client reports SMBIOS identity, fetches the
> deployment menu and the manifest from the server, downloads the WIM/ESD
> and any matched drivers, partitions the target disk and applies the
> image with `wimlib-imagex`. Driver matching (Phase 4), unattend
> generation (Phase 5) and BitLocker (Phase 12) all build on this flow.

## Sub-commands

```
autodeploy-boot identify              # log SMBIOS identity and exit
autodeploy-boot --server http://... menu      # interactive menu (default)
autodeploy-boot --server http://... deploy <image-id>
```

Common flags:

| Flag             | Meaning                                                       |
|------------------|---------------------------------------------------------------|
| `--server URL`   | AutoDeploy server base URL.                                    |
| `--sysfs PATH`   | DMI sysfs root (default `/sys/class/dmi/id`).                  |
| `--insecure-tls` | Skip TLS cert verification. **Dev only.**                      |
| `--disk DEVICE`  | Target disk for `deploy` (default `/dev/sda`).                 |
| `--work DIR`     | Scratch directory (default `/run/autodeploy`).                 |
| `--dry-run`      | Log destructive steps without executing them. Use to validate the manifest flow against a server before exposing a real machine. |

## Fail-safe behaviour

Every error path in the Boot Client exits **without touching the disk**:

- No server reachable → no imaging happens.
- Menu returns no items → no imaging happens.
- Operator cancels with `0` → no imaging happens.
- Manifest has no WIM → no imaging happens.
- Hardware identity unreadable → no imaging happens.

Only the actual `imaging.Apply` pipeline modifies the disk, and only after
the operator has explicitly selected a configuration. The firmware then
falls back to its normal boot device.

## PXE / iPXE setup

The Boot Client is delivered to target machines via iPXE chainloaded over
HTTP. AutoDeploy serves an iPXE script that points iPXE at a Linux
kernel + initramfs:

```
GET /ipxe/boot.ipxe
```

Configure your DHCP server to hand `http://<autodeploy>/ipxe/boot.ipxe` as
the bootfile URL for iPXE-aware clients. (For non-iPXE BIOS/UEFI PXE
clients, chain-load iPXE first using your existing PXE arrangement; this is
outside AutoDeploy's scope but is well documented at https://ipxe.org/.)

Put your kernel and initramfs in `AUTODEPLOY_DATA_DIR/ipxe/`:

```
$AUTODEPLOY_DATA_DIR/ipxe/
  autodeploy-kernel    # your vmlinuz
  autodeploy-initrd    # initramfs containing autodeploy-boot
```

The iPXE script chainloads them and passes `autodeploy.server=...` on the
kernel command line.

### Building the initramfs

A reference build script lives at `scripts/initramfs/build-initramfs.sh`.
It bundles the statically-linked `autodeploy-boot` binary together with the
host tools the imaging plan calls out to:

- `sgdisk` (gptfdisk) — partitioning
- `mkfs.fat` (dosfstools) — ESP filesystem
- `mkfs.ntfs` (ntfs-3g) — Windows partition filesystem
- `wimlib-imagex` (wimlib) — WIM/ESD apply and driver injection
- `mount`, `umount`, `cp`, `mkdir`, `reboot` — provided by busybox or coreutils

```sh
./scripts/initramfs/build-initramfs.sh
# Writes build/initrd.img — copy to $AUTODEPLOY_DATA_DIR/ipxe/autodeploy-initrd.
# Supply your own vmlinuz as $AUTODEPLOY_DATA_DIR/ipxe/autodeploy-kernel.
```

## What actually happens during a deploy

1. iPXE boots the kernel + initramfs.
2. `/init` parses the kernel command line, extracts the server URL, execs
   `autodeploy-boot --server <URL> menu`.
3. The client reads SMBIOS, calls `POST /api/v1/clients/menu` and renders
   the returned list. The operator picks a configuration.
4. The client GETs `/api/v1/images/{id}/manifest` and downloads every
   payload listed there (WIM/ESD, any drivers, the unattend in Phase 5).
   Range requests resume any interrupted fetches.
5. The client partitions the target disk (UEFI GPT: 100 MiB FAT32 ESP +
   remainder NTFS), applies the WIM with `wimlib-imagex`, injects matched
   driver packages, writes the unattend to `Windows\Panther\unattend.xml`,
   unmounts and reboots.

Every step is logged with `who`, `what`, `where`, `when` so a failed
deployment can be diagnosed centrally once Phase 14 (centralised log
collection) is in place. Sensitive values never appear in any log.
