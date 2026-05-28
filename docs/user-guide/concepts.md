# Concepts

A quick orientation to the objects and the flow you will work with in
AutoDeploy. The full data model and lifecycle are in the design document;
this is the short version for an operator opening the portal for the first
time.

## Objects you create

| Object             | What it is                                                |
|--------------------|-----------------------------------------------------------|
| **ISO**            | An uploaded OS install medium. Source of the WIM/ESD that gets applied to a machine. |
| **Unattend**       | A configuration object from which a complete `unattend.xml` is generated. Operators set values; they never hand-edit XML. |
| **Driver package** | A driver payload (ingested from an SCCM package) with one or more SMBIOS filters that decide which machines it applies to. |
| **Software package** | An installer with detection rules (so it is not reinstalled if already present) and an ordered list of install steps. |
| **Software loadout** | A named, ordered collection of software packages. Loadouts can inherit from other loadouts. |
| **Image**          | The composition object. An image links an ISO, an unattend and software (a loadout and/or individual packages), and may inherit from a parent image. |

## How an imaging job actually runs

1. A machine network-boots; iPXE chainloads the AutoDeploy Boot Client over HTTP.
2. The Boot Client reads hardware identity from firmware (SMBIOS) and reports it to the server.
3. If the global access PIN is enabled, the operator is prompted; the server validates it.
4. The server returns the deployable image list — plus a "re-image this machine" option if the machine is recognised in inventory.
5. The operator picks an image. The server **resolves** the effective configuration: which ISO, which unattend, which driver packages match this hardware, and what software set applies.
6. The Boot Client downloads the WIM/ESD, the matched drivers and the generated unattend over HTTP, partitions the disk, applies the image with `wimlib`, injects the drivers, writes the unattend, and reboots.
7. Windows runs unattended setup. The Deployment Client (agent) installs as part of that.
8. The agent installs the assigned software in order, skipping anything already present, enables BitLocker if a PIN is defined for the machine, and reports inventory back.
9. The server writes a dated deployment record. The machine is now in inventory and is bound to its assigned configuration.

After that, the agent stays resident and silent, checking in periodically to
pick up any bulk jobs (rename, software push, scripts) queued for the machine.

## A few rules worth remembering

- **The server decides; the client executes.** Nothing security-relevant or resolution-related is computed by the Boot Client or the agent.
- **Fail safe.** If something goes wrong — access denied, server unreachable, identity unreadable — the machine boots its existing OS. Imaging is never the default outcome of a failure.
- **ISO and unattend are nearest-wins** up the image inheritance chain. **Drivers** are matched globally by hardware fit. **Software** accumulates from loadouts and direct links. That asymmetry is deliberate.
- **No BitLocker PIN means "do not encrypt".** It is a meaningful state, not a missing setting.
- **Secrets are never logged.** PINs, recovery keys and passwords never appear in any log; the *fact* and *actor* of a secret access are.
