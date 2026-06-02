# Troubleshooting

This page lists common symptoms and where to look. The [audit log](../portal/logs.md) is your
first stop for almost everything — all components ship structured events to the server.

## The server won't start

- **No listener configured.** At least one of `AUTODEPLOY_HTTP_ADDR` or `AUTODEPLOY_HTTPS_ADDR`
  must be set. Check `/etc/default/autodeploy`.
- **HTTPS without a certificate in production.** With `AUTODEPLOY_DEV=false`, enabling
  `AUTODEPLOY_HTTPS_ADDR` requires both `AUTODEPLOY_TLS_CERT` and `AUTODEPLOY_TLS_KEY`.
- **Check the logs:** `journalctl -u autodeploy -f`.

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

## Recovery keys / BitLocker PINs can't be read

These are encrypted with the server's [secrets key](security.md#secrets-at-rest). If the key was
lost or changed, previously escrowed values cannot be decrypted. Keep the key in your
[backup plan](backup-and-retention.md).

## Still stuck?

Search the [audit log](../portal/logs.md) filtered by the affected machine, component, and time
range to see exactly what each component reported.
</content>
