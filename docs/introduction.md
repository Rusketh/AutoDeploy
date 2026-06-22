# Introduction

AutoDeploy installs and manages Windows machines over the network. You upload your Windows
media once, describe how machines should be configured, and AutoDeploy handles the rest:
network-booting each machine, imaging it, installing drivers and software, and keeping it under
management afterwards. Everything is driven from a web portal and a JSON API over HTTP(S).

![AutoDeploy dashboard](images/dashboard.png)

## The three components

AutoDeploy is made of three programs that communicate over HTTP(S). They are separate binaries
with clearly separated jobs.

### Server (`autodeploy-server`)

The server is the brain. It runs on **Linux** and provides:

- the **web portal** for operators (server-rendered, works in any modern browser),
- the **JSON API** (`/api/v1/...`) that the portal and the clients use,
- **deployment orchestration** — it resolves images and decides what each machine should do,
- a **SQLite** database and on-disk blob storage for payloads,
- an optional **built-in TFTP server** for classic PXE bootstrap.

The server is the single source of truth. Clients never decide anything on their own; they ask
the server and report back what happened.

### Boot Client (`autodeploy-boot`)

The boot client is a small **Linux** program that runs inside an initramfs, chain-loaded by iPXE
when a machine network-boots. It:

1. reads the machine's hardware identity from SMBIOS,
2. asks the server for a deployment menu (or auto-deploys if the operator has bound an image),
3. streams the Windows install media onto the local disk,
4. reboots the machine into Windows Setup.

The boot client is **fail-safe**: if anything goes wrong, it exits cleanly and the machine boots
its existing OS instead of being left in a broken state.

### Agent (`autodeploy-agent`)

The agent is a **Windows** program. It runs in two ways:

- **At deployment time**, right after Windows Setup, to install software and apply configuration.
- **As a resident Windows service** afterwards, polling the server on an interval for new work —
  software to push, a rename, a reimage, or a self-update.

The agent is delivered to each machine automatically during deployment. You never install it by
hand on target machines.

## The deployment lifecycle

A typical machine goes through these stages:

1. **Network boot.** The machine PXE/iPXE-boots and loads the AutoDeploy boot client.
2. **Identify.** The boot client reads SMBIOS identity and contacts the server. The machine
   appears in the portal [inventory](portal/machines.md) if it's new.
3. **Select or auto-deploy.** The operator picks an image from the boot menu, or the server
   returns a pre-bound image and deployment starts automatically.
4. **Stage media.** The boot client streams the Windows media defined by the chosen
   [image](portal/images.md) onto disk and reboots into Windows Setup.
5. **Windows Setup.** Windows installs using the image's [unattend](portal/payloads.md) answer
   file and matched [driver packages](portal/payloads.md#driver-packages).
6. **Configure.** The agent runs, installs the image's [software](portal/software.md), and applies
   branding.
7. **Report & remain.** The agent reports hardware and deployment results to the server and stays
   resident as a service, ready for [bulk operations](portal/bulk-operations.md).

Every step is recorded in the [audit log](portal/logs.md).

## Where to go next

- Learn the vocabulary in **[Concepts](concepts.md)**.
- Stand up a server and deploy a machine in **[Getting started](getting-started.md)**.
</content>
