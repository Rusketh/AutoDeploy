# AutoDeploy operator guide

A unified replacement for WDS / MDT / SCCM / FOG. One server, one
portal, deploys Windows machines over HTTP. Open-source, single Go
binary, no dependencies beyond your existing DHCP.

If this is your first hour with AutoDeploy, work through the
tutorials in order. Otherwise jump to the page that matches the
task at hand.

## Tutorials — first time using AutoDeploy

Work through these in order; each one assumes the previous is done.

| Step | Page | What you'll achieve |
|---|---|---|
| 1 | [Install the server](tutorial-01-install.md) | AutoDeploy running on Linux or Windows, portal reachable. |
| 2 | [Set up PXE boot](tutorial-02-pxe.md) | Target machines can netboot to AutoDeploy. **UniFi, dnsmasq, ISC dhcpd, Microsoft DHCP.** Includes the embedded-iPXE build step for routers that can't do conditional DHCP. |
| 3 | [Deploy your first machine](tutorial-03-first-deploy.md) | Take a blank VM from PXE to logged-in Windows. |
| 4 | [Add software packages](tutorial-04-add-software.md) | Make Windows installs come up with VS Code, Office, etc. already installed. |
| 5 | _Add drivers_ — coming in next docs PR | Match drivers to hardware by SMBIOS so OEM kit boots cleanly. For now see [driver-matching.md](driver-matching.md). |
| 6 | _Production: BitLocker + AD_ — coming in next docs PR | Domain-join + escrow recovery keys + per-machine PINs. For now see [bitlocker.md](bitlocker.md) and [active-directory.md](active-directory.md). |

## How-to — concrete tasks

Recipe-style. One task per page. Some of these are still in the
older feature-organized docs (linked at the bottom); the new
how-to format will land in the next docs PR.

| | |
|---|---|
| Add an ISO | See [payloads.md](payloads.md) |
| Build an image | See [getting-started.md](getting-started.md#step-7---compose-an-image) |
| Update the server | See [tutorial-01](tutorial-01-install.md) — same procedure, plus the Settings → Updates button does it from the portal. |
| Update the fleet's agents | See `docs/design/WORKLOG.md` for the most recent agent self-update entry. |
| Back up and restore | See [operations.md](operations.md#backup-and-recovery) |

## Reference — look it up

| | |
|---|---|
| [Detection rules](reference-detection-rules.md) | file, registry, MSI product code, script — when "is X already installed?" returns true. |
| [Install steps](reference-install-steps.md) | copy, unzip, msi, appx, exe, cmd, powershell — what the agent actually runs. |
| Environment variables | See [configuration.md](configuration.md) for now. |
| REST API | See [api-quickstart.md](api-quickstart.md). |

## Concepts

| | |
|---|---|
| [Concepts](concepts.md) | Five-minute orientation: ISO, Unattend, Image, Driver, Loadout, Binding. |
| [Scaling](scaling.md) | Payload mirrors, throttle, metrics, mass deployments. |

## Operations

| | |
|---|---|
| [Day-to-day operations](operations.md) | Backup, retention, security review, performance baselines. |
| [Logging](logging.md) | What's logged, search, live tail. |
| [**Troubleshooting**](troubleshooting.md) | Symptom → fix. **Start here when something's wrong.** |

## Older feature-organized docs (still accurate)

These predate the task-oriented rewrite above and may be denser
than necessary. They're still accurate; the next docs PR will
fold them into how-to and reference pages.

[Active Directory](active-directory.md) ·
[BitLocker](bitlocker.md) ·
[Boot Client](boot-client.md) ·
[Branding](branding.md) ·
[Bulk operations](bulk-operations.md) ·
[Configuration](configuration.md) ·
[Driver matching](driver-matching.md) ·
[Getting started (legacy combined walkthrough)](getting-started.md) ·
[Inventory](inventory.md) ·
[Loadouts](loadouts.md) ·
[Payloads](payloads.md) ·
[Re-imaging](reimaging.md) ·
[Security](security.md) ·
[Software packages (older single-file model)](software.md) ·
[Unattend](unattend.md) ·
[API quick-start](api-quickstart.md) ·
[Install (Linux, older detail-heavy version)](installation.md) ·
[Install (Windows)](install-windows.md) ·
[PXE setup (older — ISC dhcpd / MS DHCP / Kea, no UniFi)](pxe-setup.md)
