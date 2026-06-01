# AutoDeploy User Guide

Welcome to AutoDeploy — a network-driven platform for deploying and managing Windows machines
from a single web portal. This guide takes you from a bare server to a fully managed fleet.

> **New here?** Read the [Introduction](introduction.md) and [Concepts](concepts.md) first,
> then follow [Getting started](getting-started.md) for a complete first deployment.

## Contents

### Start here
- **[Introduction](introduction.md)** — what AutoDeploy is, the three components, and the deployment lifecycle.
- **[Concepts & glossary](concepts.md)** — ISO, Unattend, Driver, Software, Loadout, Image, Binding, Machine, Mirror.
- **[Getting started](getting-started.md)** — install the server and deploy your first machine, end to end.

### Installation
- **[Linux server install](install/linux-server.md)** — download the latest release and install the server as a service.
- **[PXE & boot setup](install/pxe-and-boot.md)** — DHCP/iPXE configuration, the boot image, and the optional built-in TFTP server.

### Using the portal
- **[Dashboard](portal/dashboard.md)** — fleet overview and quick actions.
- **[Payloads](portal/payloads.md)** — ISOs, unattend files, and driver packages.
- **[Software & loadouts](portal/software.md)** — software packages, detection, install steps, and loadouts.
- **[Images](portal/images.md)** — composing ISOs, unattends, drivers and software into a deployable image.
- **[Machines](portal/machines.md)** — inventory, machine detail, bindings, history and BitLocker.
- **[Bulk operations](portal/bulk-operations.md)** — act on many machines at once.
- **[Mirrors](portal/mirrors.md)** — site-local payload caches.
- **[Logs](portal/logs.md)** — searching and tailing the audit log.
- **[Downloads](portal/downloads.md)** — boot files and agent binaries.
- **[Settings](portal/settings.md)** — accounts, access PIN, branding, Active Directory, operational, storage, updates, PXE.

### Reference
- **[Configuration](reference/configuration.md)** — every environment variable and the data directory layout.
- **[Command-line interface](reference/cli.md)** — flags and subcommands for all three binaries.
- **[Detection rules](reference/detection-rules.md)** — how AutoDeploy decides whether software is already installed.
- **[Install steps](reference/install-steps.md)** — how software is installed.
- **[JSON API](reference/api.md)** — the full REST API behind the portal.

### Operations
- **[Security](operations/security.md)** — authentication, HTTPS, secrets at rest, the access PIN.
- **[Active Directory](operations/active-directory.md)** — domain join and lookups.
- **[BitLocker](operations/bitlocker.md)** — enabling encryption and escrowing recovery keys.
- **[Scaling](operations/scaling.md)** — mirrors, payload throttling, large fleets.
- **[Backup & retention](operations/backup-and-retention.md)** — protecting your data and pruning logs.
- **[Updates](operations/updates.md)** — updating the server and distributing new agents.
- **[Troubleshooting](operations/troubleshooting.md)** — symptoms and fixes.

---

All screenshots in this guide are captured from the AutoDeploy portal. Your portal may look
slightly different if you have customised [branding](portal/settings.md#branding) or switched
between light and dark themes.
</content>
