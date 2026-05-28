# Getting started — zero to your first deployed machine

This walks you end-to-end: download the release, install the server,
configure DHCP/PXE, upload an ISO, build an image, image a test
machine. Read top to bottom; every command is copy-pastable.

> Expect 30–60 minutes the first time. Subsequent installs are
> minutes.

## What you'll need before starting

| | Why |
|---|---|
| **A server host** | Runs AutoDeploy. Linux preferred (this guide); Windows / macOS also supported. 2 vCPU, 4 GB RAM, 100 GB disk is plenty to start. Disk grows with your ISO/driver/software library. |
| **Root / sudo on the server host** | To install the binary, create a system user, install the systemd unit, bind to privileged ports. |
| **A reachable IP / DNS name** | Target machines need to reach the server over your network. The portal is on HTTPS; PXE clients use HTTP (initially), TFTP if you enable the built-in TFTP, plus HTTPS for the actual imaging payloads. |
| **DHCP control** | You'll need to add a `next-server` / `bootfile` entry, or matching DHCP option 60/77/93 rules. If you can't change the DHCP server, you can still run AutoDeploy with an alternative DHCP proxy (out of scope here). |
| **A Windows ISO** | Whatever release you deploy: Windows 10, 11, Server 2019/2022/2025. |
| **A Linux kernel + initramfs** | The Boot Client runs in a pre-OS Linux. You can build the initramfs from the bundled script; the kernel comes from your distro of choice (or a small specialised one). |
| **A target machine or VM** | Something you can PXE-boot and don't mind wiping. A VM with PXE enabled in firmware is perfect for the first deploy. |

## Step 1 — Download the release

Visit `https://github.com/Rusketh/AutoDeploy/releases` and grab the latest
`v*` tag. You want these files:

| File | What for |
|------|----------|
| `autodeploy-server-linux-amd64` (or `-arm64`, `-darwin-*`, `-windows-amd64.exe`) | The server binary. Match your server host's OS/arch. |
| `autodeploy-boot-linux-amd64` (or `-arm64`) | Goes into the initramfs that PXE clients boot. |
| `autodeploy-agent-windows-amd64.exe` (or `-arm64.exe`) | The in-OS agent that runs after Windows installs. |
| `autodeploy-extras.tar.gz` | Operator scripts and the user guide for this exact release. |
| matching `*.sha256` files | Verify each download. |

```sh
# Replace v0.1.0 with the actual tag you're installing.
TAG=v0.1.0
URL=https://github.com/Rusketh/AutoDeploy/releases/download/$TAG
mkdir -p ~/autodeploy && cd ~/autodeploy

curl -LO $URL/autodeploy-server-linux-amd64
curl -LO $URL/autodeploy-server-linux-amd64.sha256
sha256sum -c autodeploy-server-linux-amd64.sha256

curl -LO $URL/autodeploy-boot-linux-amd64
curl -LO $URL/autodeploy-boot-linux-amd64.sha256
sha256sum -c autodeploy-boot-linux-amd64.sha256

curl -LO $URL/autodeploy-agent-windows-amd64.exe
curl -LO $URL/autodeploy-agent-windows-amd64.exe.sha256
sha256sum -c autodeploy-agent-windows-amd64.exe.sha256

curl -LO $URL/autodeploy-extras.tar.gz
curl -LO $URL/autodeploy-extras.tar.gz.sha256
sha256sum -c autodeploy-extras.tar.gz.sha256

tar xzf autodeploy-extras.tar.gz   # → scripts/ and docs/
```

If `sha256sum -c` says anything other than `OK`, **stop** and re-download.

## Step 2 — Install the server (Linux)

The release ships an installer that places the binary, creates a
system user, installs a systemd unit, and grabs the iPXE bootstrap
binaries. From the directory you downloaded into:

```sh
sudo ./scripts/install-linux.sh
```

Output:

```
== Installing autodeploy-server ==
== Creating service account 'autodeploy' ==
== Creating /var/lib/autodeploy ==
== Installing systemd unit ==
== Installing /etc/default/autodeploy (edit me) ==
== Fetching iPXE bootstrap binaries ==
Fetching http://boot.ipxe.org/undionly.kpxe -> /var/lib/autodeploy/ipxe/undionly.kpxe
…
============================================================
AutoDeploy is installed. Next steps: …
============================================================
```

What the installer did:

- Copied the binary to `/usr/local/bin/autodeploy-server`.
- Created a system user `autodeploy` (no login, no shell).
- Created `/var/lib/autodeploy/` (the data directory) owned by that user.
- Installed `/etc/systemd/system/autodeploy.service` and an example
  `/etc/default/autodeploy` you'll edit next.
- Downloaded the iPXE bootstrap binaries to
  `/var/lib/autodeploy/ipxe/` so the built-in TFTP can serve them.

### Installing on Windows or macOS

Same idea, no script. Place the matching binary somewhere on `$PATH`
(`C:\Program Files\AutoDeploy\autodeploy-server.exe` on Windows,
`/usr/local/bin/autodeploy-server` on macOS), create a data directory,
and configure either an NSSM service (Windows) or a launchd plist
(macOS). See [installation.md](installation.md) for the env-var list.

## Step 3 — Configure the server

Open `/etc/default/autodeploy` and review:

```sh
sudo nano /etc/default/autodeploy
```

The bits that matter for a first install:

```sh
# Pick the surfaces you need.
AUTODEPLOY_HTTP_ADDR=127.0.0.1:8080        # loopback HTTP only
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443           # portal + payload over HTTPS
AUTODEPLOY_TFTP_ADDR=:69                    # built-in TFTP for PXE

# Production: stricter rules. Set to false once you've verified the
# server starts and the portal loads.
AUTODEPLOY_DEV=false

# Generate this ONCE and never lose it. Encrypts BitLocker PINs and
# recovery keys at rest. Losing it = losing those secrets.
AUTODEPLOY_SECRETS_KEY=<output of: openssl rand -hex 32>
```

Generate the secrets key:

```sh
openssl rand -hex 32
# 86f1c6f7…   ← paste this whole string into AUTODEPLOY_SECRETS_KEY
```

**Back up the key somewhere off the AutoDeploy host** — a password
manager, a vault, your usual secret store. Without it, escrowed
BitLocker PINs and recovery keys are unrecoverable from a backup
of the database alone.

### TLS for HTTPS

Production needs a real cert. Easiest paths:

- **Internal CA**: have your CA sign a cert for the AutoDeploy host's
  DNS name, save the PEM-encoded cert and key.
- **Let's Encrypt** (if the host is internet-reachable on port 80):
  `certbot certonly --standalone -d autodeploy.corp.example`
- **Self-signed for first run only**: leave `AUTODEPLOY_TLS_CERT` and
  `AUTODEPLOY_TLS_KEY` blank with `AUTODEPLOY_DEV=true`; the server
  will generate one under `/var/lib/autodeploy/tls/`. Replace before
  going live; browsers will complain otherwise.

Place the cert/key and point the config at them:

```sh
sudo mkdir -p /etc/autodeploy/tls
sudo install -m 0644 server.crt /etc/autodeploy/tls/server.crt
sudo install -m 0600 -o autodeploy server.key /etc/autodeploy/tls/server.key
# Uncomment in /etc/default/autodeploy:
AUTODEPLOY_TLS_CERT=/etc/autodeploy/tls/server.crt
AUTODEPLOY_TLS_KEY=/etc/autodeploy/tls/server.key
```

## Step 4 — Start the server

```sh
sudo systemctl enable --now autodeploy
sudo systemctl status autodeploy
```

A working unit looks like:

```
● autodeploy.service - AutoDeploy server
     Loaded: loaded (/etc/systemd/system/autodeploy.service; enabled; preset: enabled)
     Active: active (running) since …
   Main PID: 12345 (autodeploy-serv)
     Memory: 18M
        CPU: 0.5s
        Tasks: 8
        CGroup: /system.slice/autodeploy.service
                └─12345 /usr/local/bin/autodeploy-server
```

Tail the logs:

```sh
sudo journalctl -u autodeploy -f
```

You should see something like:

```
auth.bootstrap_admin password_file=/var/lib/autodeploy/admin-bootstrap.txt
server.start target=0.0.0.0:443 https_addr=0.0.0.0:443 …
http.listen addr=0.0.0.0:443 dev_mode=false
tftp.listen addr=:69 root=/var/lib/autodeploy/ipxe
```

If `https.listen` is missing, the cert/key didn't load — check the
path and permissions. The `autodeploy` user must be able to read
both files.

### Troubleshooting

| Symptom | Likely fix |
|---------|------------|
| `permission denied` binding to :443 / :69 | The unit grants `CAP_NET_BIND_SERVICE`; if your distro doesn't honour the AmbientCapabilities directive, run on a high port (`:8443`, `:6969`) and front with a reverse proxy or `iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 8443`. |
| `cleartext HTTP refused in production mode` | `AUTODEPLOY_DEV=true` then restart; or bind HTTP to `127.0.0.1` only. |
| Portal returns 502 / nothing | Service crashed; `journalctl -u autodeploy -n 200` |

## Step 5 — Log in and change the admin password

The first start created a random admin password and wrote it to disk:

```sh
sudo cat /var/lib/autodeploy/admin-bootstrap.txt
# username: admin
# password: bHjVQPyGOXyy_FJ1-8M8Ug
# # Read this once, log in, change the password, then delete this file.
```

Open `https://autodeploy.corp.example/portal/` in a browser. Log in
as `admin` with that password.

Now **change it**:

1. Click **Settings** → **Local accounts**.
2. In the row for `admin`, type a new password and click **Set password**.

Now **delete the file**:

```sh
sudo rm /var/lib/autodeploy/admin-bootstrap.txt
```

Make a few more accounts if other operators will use the system.
There's no graded permission model — every account has full access.

## Step 6 — Build the Boot Client initramfs

The Boot Client is a small Linux binary that PXE clients chainload
into. It needs to live in an initramfs alongside the host tools the
imaging plan uses (`sgdisk`, `mkfs.fat`, `mkfs.ntfs`, `wimlib-imagex`,
`busybox`).

On a Linux build host (can be the AutoDeploy host itself):

```sh
sudo apt install -y gdisk dosfstools ntfs-3g wimtools busybox-static cpio
# Or the equivalent for your distro.

cd ~/autodeploy
./scripts/initramfs/build-initramfs.sh
# Writes build/initrd.img
```

Place the result and a Linux kernel where the Boot Client iPXE script
looks for them:

```sh
sudo cp build/initrd.img /var/lib/autodeploy/ipxe/autodeploy-initrd

# A small kernel works fine. Grab the one your distro shipped:
sudo cp /boot/vmlinuz-$(uname -r) /var/lib/autodeploy/ipxe/autodeploy-kernel
# OR build/download a minimal one for your hardware.

sudo chown autodeploy:autodeploy /var/lib/autodeploy/ipxe/*
```

> **Tip**: most modern distro kernels (Debian, Ubuntu, RHEL, Fedora,
> Arch generic) have the network and disk drivers you need built in
> or as modules. Use the same kernel your build host runs unless the
> target hardware needs something exotic.

## Step 7 — Configure DHCP

The pattern is: **firmware PXE gets the iPXE binary; iPXE itself gets
the HTTP URL of AutoDeploy's chainload script**.

### Option A — ISC dhcpd

Add this to your subnet definition (substitute your IPs / hostname):

```
option arch code 93 = unsigned integer 16;

subnet 10.10.10.0 netmask 255.255.255.0 {
    range 10.10.10.100 10.10.10.200;
    option routers 10.10.10.1;
    next-server 10.10.10.5;   # AutoDeploy host (it serves TFTP too)

    if exists user-class and option user-class = "iPXE" {
        # Second pass: iPXE itself is asking. Hand it the HTTP URL.
        filename "http://autodeploy.corp.example/ipxe/boot.ipxe";
    } elsif option arch = 00:07 or option arch = 00:09 {
        # First pass, UEFI x64 firmware.
        filename "ipxe.efi";
    } elsif option arch = 00:0b {
        # First pass, UEFI arm64.
        filename "ipxe-arm64.efi";
    } else {
        # First pass, legacy BIOS PXE.
        filename "undionly.kpxe";
    }
}
```

Restart `dhcpd`. Verify:

```sh
sudo journalctl -u isc-dhcp-server -f
# You should see DHCPDISCOVER → DHCPOFFER → DHCPREQUEST → DHCPACK
# on the test machine's MAC when it powers on.
```

### Option B — Microsoft DHCP

DHCP console → right-click your scope → **Configure Options** →
**Advanced**:

1. **066 Boot Server Host Name** = AutoDeploy host's DNS name or IP
2. **067 Bootfile Name** = `undionly.kpxe` (for BIOS) or
   `ipxe.efi` (for UEFI). If your fleet is mixed, create separate
   scope-option entries scoped by **Vendor Class** or **User Class**
   so iPXE clients get the HTTP URL on the second pass.

See `docs/user-guide/pxe-setup.md` for the full Microsoft DHCP walkthrough.

### Option C — Kea, dnsmasq, others

`docs/user-guide/pxe-setup.md` has Kea. Dnsmasq is a single
`dhcp-boot=tag:bios,undionly.kpxe` + `dhcp-boot=tag:efi-x86_64,ipxe.efi`
+ a `dhcp-userclass=set:ipxe,iPXE` tag + a `dhcp-boot=tag:ipxe,http://…`.
Pattern is the same: firmware → iPXE binary, iPXE → HTTP URL.

## Step 8 — Verify PXE works

Power on your test machine with PXE enabled in firmware. Watch the
sequence:

1. Firmware prints "PXE-E… looking for DHCP" then receives the lease.
2. Firmware TFTPs the boot file from AutoDeploy. (`tftp.read.ok`
   should appear in `journalctl -u autodeploy -f`.)
3. iPXE comes up, prints `iPXE initialising devices…`, gets DHCP
   itself, fetches `http://autodeploy.corp.example/ipxe/boot.ipxe`.
4. iPXE loads the kernel + initramfs over HTTP.
5. The Linux Boot Client starts and connects to the API. The portal's
   **Machines** page now shows a new row with the machine's SMBIOS UUID.
6. The operator sees the menu: a list of deployable images (empty
   right now). Pressing `0` cancels and the machine boots normally.

If you got this far, **the bootstrap is working**. You just don't
have any images to deploy yet.

### If anything fails

| Stops at… | Most likely cause |
|-----------|-------------------|
| `PXE-E53: No boot filename received` | DHCP isn't returning option 67. Check the subnet config. |
| TFTP timeout | UDP/69 blocked, or AutoDeploy not running on the TFTP address you configured. |
| iPXE chainloads but HTTP errors | `autodeploy.corp.example` resolves wrong, or the cert is self-signed and iPXE doesn't trust it. Use `chain http+://…` (HTTP first) for the iPXE script during initial setup. |
| Kernel panics | Missing driver for the target NIC or disk. Use a more inclusive kernel. |
| AutoDeploy doesn't see the machine in Inventory | Boot Client can't reach the API. Firewall? DNS? Check the Boot Client console output. |

## Step 9 — Upload your first ISO

In the portal:

1. **ISOs** → **New ISO**
2. Name: `Win11`, OS type: `windows-11`, Description: anything.
   **Create**.
3. The page reloads to the edit view. **Upload ISO file** → pick your
   `Win11_24H2_English_x64.iso` (or whichever) → **Upload**.
   The file streams to disk — even a 6 GB ISO works fine.
4. When the upload finishes, click **Extract contents**. The server
   walks the ISO and writes `install.wim` / `install.esd` and the
   supporting files into `/var/lib/autodeploy/iso/1/files/`.

Verify the ISO row now shows a `Storage path` like
`iso/1/files/sources/install.wim`.

## Step 10 — Create an Unattend

1. **Unattends** → **New unattend**.
2. The form is a guided walk through ten sections (use the left-side
   TOC):
   - **Identity** — name (e.g. `lab-default`) and description.
   - **Target OS** — pick **Windows 11** (or 10 / Server). This
     drives the generator: Windows 11 gets `BypassNRO` automatically
     so OOBE doesn't insist on a Microsoft Account.
   - **Regional** — pick locale, keyboard, time zone from the
     dropdowns. Don't type codes; the catalog has the common ones.
   - **Edition** — auto-filtered to the target OS.
   - **Local administrator** — username, password (this lands in
     the XML; never logged).
   - **Computer naming** — `random` is fine for the first deploy;
     change later via bulk rename.
   - **OOBE** — defaults skip everything. Leave them.
   - **Windows 11 options** — leave **BypassNRO** on. **Bypass
     Windows 11 hardware requirements** only if your test machine
     doesn't meet them.
   - **Domain join** — skip unless your lab is in an AD domain.
   - **First-logon commands** — leave empty for now. The agent
     bootstrap is appended automatically.
3. **Create**.
4. On the edit page click **Preview XML**. Scan it. Check:
   - The header comment says `Generated by AutoDeploy for windows-11`.
   - Your time zone and locale are in there.
   - The last `<SynchronousCommand>` runs `autodeploy-agent.exe`.

## Step 11 — Compose your first Image

1. **Images** → **New image**.
2. Name: `win11-lab`. Description: anything.
3. **ISO**: pick `Win11`.
4. **Unattend**: pick `lab-default`.
5. Leave loadout, parent, software empty for now.
6. **Create**.

On the edit page click **View resolved** to see exactly what would
be deployed:

```
Inheritance chain: win11-lab
ISO (nearest-wins): Win11 / windows-11
Unattend (nearest-wins, used in full): lab-default
Software: (none)
```

## Step 12 — Deploy a machine

Power on the test machine again. The PXE bootstrap runs through the
same chain (steps 1–4 above), but **this time the menu has an
entry**: `win11-lab`.

Select it. The Boot Client:

1. POSTs identity to `/api/v1/images/1/manifest`.
2. Server resolves the configuration and returns the manifest with
   URLs for the WIM and the (generated) unattend.xml.
3. The Boot Client downloads each URL, partitions the disk
   (UEFI GPT: 100 MiB FAT32 ESP + remainder NTFS), applies the WIM
   with `wimlib-imagex`, writes the unattend to
   `Windows\Panther\unattend.xml`, and reboots.
4. Windows installs unattended. On first logon, the AutoDeploy
   agent starts and reports back.

The portal's **Machines** page now shows the machine with a
**deployment history** entry transitioning from `in_progress` to
`ok`.

## Step 13 — Bind the machine for re-imaging

While you're on the machine's detail page:

1. Set **Machine name** to `LAB-01` (or whatever you want).
2. **Bound image**: pick `win11-lab`.
3. **Save binding**.

Next time this machine PXE-boots into AutoDeploy, the menu will
offer a **Re-image** option referencing `win11-lab`. Re-imaging
always uses the **current** definition (updated ISO, updated
unattend, current driver matches) — never a frozen historical
snapshot.

## Where to go next

You have an end-to-end deployment working. From here:

| You want to… | Read |
|--------------|------|
| Add drivers automatically matched to hardware | [driver-matching.md](driver-matching.md) |
| Install software automatically after Windows imaging | [software.md](software.md) |
| Group software into reusable loadouts | [loadouts.md](loadouts.md) |
| Push scripts / renames / installs to deployed machines | [bulk-operations.md](bulk-operations.md) |
| Manage many sites with their own bandwidth-local mirrors | [scaling.md](scaling.md) |
| Turn on BitLocker on deployed machines | [bitlocker.md](bitlocker.md) |
| Configure Active Directory delete-and-replace | [active-directory.md](active-directory.md) |
| Customise the portal / OEM info with your branding | [branding.md](branding.md) |
| Run a long-term-stable production setup | [operations.md](operations.md) |

## Common first-deploy gotchas

| Symptom | Fix |
|---------|-----|
| PXE-booted machine sits at the menu — no images listed | You created the Image but not the ISO link. Edit the image, set ISO. |
| Deploy starts, then fails at `wimlib-imagex apply` | The initramfs is missing `wimlib-imagex`. Re-run `build-initramfs.sh` and check the script's "missing tools" warning. |
| Windows installs but never reboots into OOBE silent | The unattend XML has a typo — open the unattend in the portal and re-check Preview XML. |
| Agent never reports after Windows installs | Network is up but the agent can't reach the AutoDeploy URL. Check DNS in the deployed image, or set `autodeploy.server=` directly in the unattend's first-logon command. |
| `Machines` page is empty even after PXE-boot | Boot Client crashed before hitting the API. Add a serial console (already set in the iPXE script: `console=ttyS0,115200`) and watch the boot. |

Once you can image a test machine end-to-end, you have the spine. The
rest of the system layers on top: drivers, software, AD, BitLocker,
bulk operations, mirrors — all configured from the same portal.
