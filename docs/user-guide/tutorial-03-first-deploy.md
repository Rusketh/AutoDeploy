# Tutorial 3 — Deploy your first machine

30 minutes. Take a blank VM (or a bare-metal box you don't mind
wiping) from PXE to logged-in Windows.

Prerequisites:
- [Tutorial 1](tutorial-01-install.md) — server installed.
- [Tutorial 2](tutorial-02-pxe.md) — target can PXE boot and reach
  the AutoDeploy boot menu.

## What you'll do

1. Upload a Windows ISO.
2. Create an **unattend** — answers Windows' setup questions
   automatically.
3. Compose them into an **image** — the deployable unit.
4. PXE-boot the target, pick the image, watch Windows install.

## Step 1 — Upload a Windows ISO

You'll need an `.iso` of any modern Windows: 10, 11, Server
2019/2022/2025. From Microsoft directly is fine; OEM-bundled is
fine; volume-licensed is fine.

In the portal:

1. **ISOs** in the top nav.
2. **+ New ISO**.
3. Fill in:
   - **Name** — something readable like "Windows 11 23H2 Pro x64".
   - **OS type** — `windows-11` (or `windows-10` / `windows-server-2022`).
   - **Description** — optional, free text.
4. Save → you land on the edit page.
5. **Upload ISO file** — pick the `.iso`. The progress bar shows
   live byte count and ETA; multi-GB uploads are normal.
6. After upload completes, click **Extract contents**. This walks
   the ISO and finds the WIM/ESD inside (typically
   `sources/install.wim` or `sources/install.esd`). You'll see
   "Indexed N editions" — Pro, Home, Enterprise, etc.
7. Pick which **Edition** to deploy (most common: Pro).

The ISO is now ready. It'll be served to target machines from
`/payload/iso/{id}/`.

## Step 2 — Create an unattend

The unattend tells Windows Setup what to do — language, account,
hostname, disk layout, network. AutoDeploy ships a 15-section
structured editor that generates the XML for you; you never
write `<unattend xmlns="…">` by hand.

In the portal:

1. **Unattends** → **+ New unattend**.
2. **Name**: "Lab default" (or whatever).
3. Fill in the sections that matter:

| Section | Recommended for first deploy |
|---|---|
| **General** | Region: your country. Keyboard: your layout. Timezone: yours. |
| **Computer name** | Use a template like `LAB-%SERIAL_LAST6%` so each machine gets a unique name from its SMBIOS serial. |
| **Local admin** | Create a local admin user (e.g. `localadmin`) with a strong password. **You'll need this to log in if AD join fails.** |
| **Disk partitioning** | "Wipe and use whole disk" is fine for the first test. |
| **Network** | Leave at DHCP. |
| **OOBE skips** | Tick all of them — Microsoft Account, Cortana, OEM EULA, privacy questions. Makes the install run to the desktop without human input. |
| **First-logon commands** | Leave empty for now. |

Skip the rest (AD join, BitLocker, etc.) for the first deploy.
We'll come back for those later — for now see
[bitlocker.md](bitlocker.md) and
[active-directory.md](active-directory.md).

4. Save.

## Step 3 — Compose an image

An **image** is the thing you deploy: it ties an ISO + edition +
unattend together, plus any drivers and software you want
installed.

1. **Images** → **+ New image**.
2. **Name**: "Lab Windows 11".
3. **ISO**: pick the one you uploaded.
4. **Edition**: e.g. "Pro".
5. **Unattend**: pick the one from step 2.
6. **Drivers**, **Software**, **Loadouts**: leave empty for the
   first deploy.
7. Save.

The image is now resolvable. The boot menu on every PXE-booted
target will offer it.

## Step 4 — PXE boot the target

Power on the target machine. After DHCP and iPXE, you should
land on the AutoDeploy boot menu:

```
AutoDeploy
══════════════════════════════════════════════════════════════
This machine has not been seen before.
  Serial:        VMware-56-4d-…
  UUID:          422e4f97-…
  Manufacturer:  VMware, Inc.
  Product:       VMware20,1

Pick an image to deploy:
  [1] Lab Windows 11
  [q] Exit to firmware
?
```

Press `1`. The Boot Client downloads the WIM, applies it,
installs the bootloader, reboots into Windows Setup. Total time
for a Windows 11 install over a 1 Gbps LAN: roughly 10-15
minutes.

You'll see the standard Windows install screens flash by, OOBE
will auto-skip (because the unattend handled it), and you'll
land at the logon screen with your `localadmin` user ready.

## Step 5 — Verify

Log in as `localadmin`. Check:

| | |
|---|---|
| **Hostname** | `LAB-<serial-suffix>` (or whatever your template produced). |
| **Locale / timezone** | What you set in the unattend. |
| **Network** | Got a DHCP lease. |
| **Disk** | Single C: volume using the whole disk. |

Back in the AutoDeploy portal:

1. **Machines** — the target shows up with its SMBIOS info.
2. The **Image** column shows "Lab Windows 11".
3. The **Last deployed at** column shows the timestamp.

## What's next

| | |
|---|---|
| Add VS Code, Office, etc. to the install | [Tutorial 4 — Add software packages](tutorial-04-add-software.md) |
| Make OEM hardware boot cleanly (drivers) | [driver-matching.md](driver-matching.md) (older docs; a tutorial format is coming) |
| Production: BitLocker, AD join | [bitlocker.md](bitlocker.md) and [active-directory.md](active-directory.md) (older docs; a tutorial format is coming) |

## If something didn't work

| Symptom | Fix |
|---|---|
| Boot menu shows but no images | The image isn't matching this machine. Check **Images** → your image → it should have an ISO+unattend; if it has SMBIOS filters they have to match. |
| WIM download hangs / errors | Check the Boot Client logs in **Logs → Live tail**. Usually a firewall or proxy mangling the HTTP Range requests. |
| Windows installs but fails OOBE | Almost always a malformed unattend. The Boot Client logs the generated XML; copy it out and validate with Microsoft's `Windows-Setup-Validator`. |
| Machine boots from disk instead of network | BIOS/UEFI boot order. Force network with F12 (or the equivalent) for the first deploy; after deploy, boot order back to disk-first. |
