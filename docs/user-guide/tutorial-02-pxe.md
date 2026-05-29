# Tutorial 2 — Set up PXE boot

20 minutes. After this, target machines on your network can PXE
boot to AutoDeploy. Covers **UniFi**, **dnsmasq**, **ISC dhcpd**,
**Microsoft DHCP** — pick the one that matches your environment.

Prerequisite: [Tutorial 1](tutorial-01-install.md) is done and the
server is running.

## What's about to happen

When a target machine powers on with PXE enabled in firmware:

1. It broadcasts a DHCP request asking for both an IP and a boot
   file.
2. Your DHCP server hands back an IP + the address of a TFTP
   server + the name of a "first-stage" boot file.
3. The machine TFTP-fetches the first-stage boot file. We use
   **iPXE** — small ~75 KB / 1 MB binary that knows how to boot
   over HTTP.
4. iPXE chainloads to `http://<autodeploy>/ipxe/boot.ipxe` —
   AutoDeploy's bootstrap script — and from there fetches the
   Linux kernel + initramfs that runs the **Boot Client**.
5. The Boot Client identifies the machine to AutoDeploy, picks an
   image, deploys.

The tricky part is step 4. Two approaches:

- **Two-stage DHCP (the classic pattern)**: DHCP serves
  `ipxe.efi` to firmware-PXE clients and `http://...ipxe` to
  iPXE clients. Works perfectly on DHCP servers that can
  differentiate (ISC dhcpd, Microsoft DHCP with Vendor Classes,
  Kea, dnsmasq). UniFi and most consumer routers **can't** do
  this.
- **Embedded iPXE**: build an iPXE binary with the AutoDeploy
  URL baked in. DHCP just hands out the binary; iPXE
  self-bootstraps. Works with any DHCP that can hand back a
  TFTP server + filename. **This is the path UniFi users want.**

Both are covered below.

## Step 1 — Make sure AutoDeploy is serving the boot files

The installer set this up. Verify:

```sh
sudo systemctl status autodeploy
journalctl -u autodeploy -n 50 --no-pager | grep tftp.listen
# Expect: tftp.listen addr=:69 root=/var/lib/autodeploy/ipxe

ls /var/lib/autodeploy/ipxe/
# undionly.kpxe     ← BIOS PXE chainload
# ipxe.pxe          ← BIOS PXE chainload, alternative if undionly.kpxe is flaky
# ipxe.efi          ← UEFI x64
# snponly.efi       ← UEFI x64 fallback (use if ipxe.efi can't bring NIC up)
# ipxe-arm64.efi    ← UEFI arm64
# (autodeploy-kernel and autodeploy-initrd land here after step 3 below)
```

If `ipxe.efi` and `snponly.efi` are missing, fetch them manually
from `http://boot.ipxe.org/x86_64-efi/` (see
[troubleshooting](troubleshooting.md#pxe)) or re-run
`sudo /etc/autodeploy/scripts/fetch-ipxe.sh` from the release
bundle.

Smoke-test the TFTP server from another box on the LAN:

```sh
tftp <autodeploy-ip>
tftp> get ipxe.efi
Received 1182208 bytes in 0.1 seconds
tftp> quit
```

If that round-trips, the AutoDeploy side is ready.

## Step 2 — Pick your DHCP path

### Option A: I'm on UniFi (or any router that can't conditional-DHCP)

Skip ahead to [Step 2A — Embedded iPXE](#step-2a---embedded-ipxe)
and the [UniFi config](#step-2a-3-configure-unifi).

### Option B: I'm on ISC dhcpd, Microsoft DHCP, Kea, or dnsmasq

Go to [Step 2B — Two-stage DHCP](#step-2b---two-stage-dhcp-isc--microsoft--kea--dnsmasq).
Two-stage is more flexible: BIOS clients get `undionly.kpxe`,
UEFI x64 clients get `ipxe.efi`, UEFI arm64 clients get
`ipxe-arm64.efi`, all on the same network.

## Step 2A — Embedded iPXE

The fix for "my DHCP can't differentiate firmware from iPXE": build
iPXE with the AutoDeploy URL **embedded inside the binary**. DHCP
hands out the binary; iPXE runs, ignores DHCP's bootfile, and
follows the embedded URL.

### Step 2A.1 — Run the build script

```sh
sudo /etc/autodeploy/scripts/build-embedded-ipxe.sh
```

Output:

```
== Embedded iPXE build ==
  AutoDeploy URL: http://192.168.20.60:8080
  Target dir:     /var/lib/autodeploy/ipxe/

== Installing build toolchain (build-essential liblzma-dev) ==
[... apt-get noise ...]
== Cloning iPXE into /tmp/ipxe-build-XXXXXX ==

== Embedded script ==
    #!ipxe
    echo Loaded embedded AutoDeploy bootstrap
    ifstat ||
    dhcp ||
    echo Chainloading http://192.168.20.60:8080/ipxe/boot.ipxe ...
    chain --replace --autofree http://192.168.20.60:8080/ipxe/boot.ipxe ||
    echo === iPXE shell (chainload failed) ===
    shell

== Building bin/undionly.kpxe (-> undionly.kpxe) ==
    installed /var/lib/autodeploy/ipxe/undionly.kpxe
== Building bin/ipxe.pxe (-> ipxe.pxe) ==
    installed /var/lib/autodeploy/ipxe/ipxe.pxe
== Building bin-x86_64-efi/ipxe.efi (-> ipxe.efi) ==
    installed /var/lib/autodeploy/ipxe/ipxe.efi
== Building bin-x86_64-efi/snponly.efi (-> snponly.efi) ==
    installed /var/lib/autodeploy/ipxe/snponly.efi
[arm64 skipped unless gcc-aarch64-linux-gnu is installed]

== Done ==
```

Build takes 2-3 minutes the first time (and pulls in the
build-essential apt packages once). The script:

- Auto-detects the AutoDeploy URL from `/etc/default/autodeploy`
  (`AUTODEPLOY_HTTP_ADDR` and the host's primary IP). Pass an
  explicit URL like `sudo build-embedded-ipxe.sh http://my-host:8080`
  to override.
- Installs `build-essential`, `git`, `liblzma-dev` if not already
  present.
- Clones iPXE shallow.
- Writes a 7-line embedded boot script.
- Builds five iPXE binaries (or four, without arm64).
- Drops them into `/var/lib/autodeploy/ipxe/`.

No service restart needed — TFTP reads from disk live.

### Step 2A.2 — Drop the Boot Client kernel + initramfs in place

```sh
sudo /etc/autodeploy/scripts/initramfs/build-initramfs.sh
sudo cp /boot/vmlinuz-$(uname -r) /var/lib/autodeploy/ipxe/autodeploy-kernel
sudo chown autodeploy:autodeploy /var/lib/autodeploy/ipxe/autodeploy-kernel
```

(Skip this if you've already done it. iPXE chains to AutoDeploy's
HTTP `boot.ipxe`, which references these two files.)

### Step 2A.3 — Configure UniFi

UniFi has a built-in "Network Boot" feature. With embedded iPXE,
you only need it to hand out the binary — no conditional logic
needed.

**Where the setting is:**

| UniFi controller | Path |
|---|---|
| UniFi Network 8.x (UDM-Pro, UDR, UCG-Max, Cloud Gateway Ultra) | Settings → Networks → *[your LAN]* → **Advanced** → DHCP Service → **Network Boot** |
| UniFi Network 7.x and older | Settings → Networks → *[your LAN]* → **DHCP** → Network Boot |

**Fields:**

| Field | Value |
|---|---|
| Enable Network Boot | **on** |
| Next Server | LAN IP of your AutoDeploy host (e.g. `192.168.1.5`) |
| Boot File | depends on the fleet — see below |

**Boot file by fleet:**

| Fleet | Boot File |
|---|---|
| Modern UEFI x64 (~2015+, most homelab gear) | `ipxe.efi` |
| UEFI x64 where `ipxe.efi` can't bring the NIC up | `snponly.efi` |
| Legacy BIOS | `undionly.kpxe` |
| Hyper-V Gen2 specifically | `snponly.efi` (Hyper-V's synthetic NIC prefers SNP-only) |

UniFi only accepts ONE boot file per network. Mixed fleets need
either separate VLANs or the [dnsmasq proxy](#mixed-fleets-on-unifi---dnsmasq-as-proxy)
approach below.

### Step 2A.4 — Configure other DHCP servers (same pattern)

The pattern is the same on any DHCP server: set `next-server` to
the AutoDeploy IP, `bootfile` to whichever iPXE binary matches
your fleet.

| DHCP server | Where |
|---|---|
| OPNsense / pfSense | Services → DHCPv4 → *[interface]* → Network Booting |
| OpenWrt | LuCI → Network → DHCP and DNS → "TFTP server" section |
| MikroTik RouterOS | IP → DHCP Server → Network → fill `next-server` and `boot-file-name` |

### Mixed fleets on UniFi — dnsmasq as proxy

UniFi UI gives ONE boot file per network. If you have BIOS +
UEFI on the same VLAN and don't want to split VLANs, run
dnsmasq in **proxy-DHCP mode** alongside UniFi:

```ini
# /etc/dnsmasq.d/autodeploy.conf
dhcp-range=192.168.1.0,proxy
dhcp-no-override
dhcp-match=set:bios,option:client-arch,0
dhcp-match=set:uefi64,option:client-arch,7
dhcp-match=set:uefi64,option:client-arch,9
dhcp-match=set:uefiarm,option:client-arch,11
dhcp-boot=tag:bios,undionly.kpxe,,192.168.1.5
dhcp-boot=tag:uefi64,ipxe.efi,,192.168.1.5
dhcp-boot=tag:uefiarm,ipxe-arm64.efi,,192.168.1.5
```

Replace `192.168.1.0` with your subnet, `192.168.1.5` with the
AutoDeploy host. UniFi keeps doing IP leases; dnsmasq handles
just the PXE side per-arch. (With embedded iPXE, you don't even
need the `user-class iPXE` branch.)

## Step 2B — Two-stage DHCP (ISC / Microsoft / Kea / dnsmasq)

For DHCP servers that CAN differentiate firmware from iPXE.
Hands firmware clients the iPXE binary; hands iPXE clients the
AutoDeploy HTTP URL.

This path lets you use the **prebuilt** iPXE binaries from
`boot.ipxe.org` — no embedded build needed.

### ISC dhcpd

```
# /etc/dhcp/dhcpd.conf
option arch code 93 = unsigned integer 16;
option user-class code 77 = string;

subnet 192.168.1.0 netmask 255.255.255.0 {
    range 192.168.1.100 192.168.1.200;
    option routers 192.168.1.1;
    next-server 192.168.1.5;   # AutoDeploy

    if exists user-class and option user-class = "iPXE" {
        filename "http://192.168.1.5:8080/ipxe/boot.ipxe";
    } elsif option arch = 00:07 or option arch = 00:09 {
        filename "ipxe.efi";
    } elsif option arch = 00:0b {
        filename "ipxe-arm64.efi";
    } else {
        filename "undionly.kpxe";
    }
}
```

Restart: `sudo systemctl restart isc-dhcp-server`.

### Microsoft DHCP

DHCP console → right-click the scope → **Configure Options** →
**Advanced** tab:

| Option | Value |
|---|---|
| 066 (Boot Server Host Name) | AutoDeploy IP |
| 067 (Bootfile Name) | `ipxe.efi` |

For arch-based and iPXE-class branching, create Vendor Classes
matching the client's DHCP option 60/93 and override option 067
per class. Microsoft's how-to:
<https://learn.microsoft.com/en-us/windows-server/networking/technologies/dhcp/dhcp-pxe-boot>.

### dnsmasq (full DHCP, not proxy)

```ini
# /etc/dnsmasq.d/autodeploy.conf
dhcp-range=192.168.1.100,192.168.1.200,12h
dhcp-match=set:bios,option:client-arch,0
dhcp-match=set:uefi64,option:client-arch,7
dhcp-match=set:uefi64,option:client-arch,9
dhcp-match=set:uefiarm,option:client-arch,11
dhcp-userclass=set:ipxe,iPXE
dhcp-boot=tag:ipxe,http://192.168.1.5:8080/ipxe/boot.ipxe
dhcp-boot=tag:!ipxe,tag:bios,undionly.kpxe,,192.168.1.5
dhcp-boot=tag:!ipxe,tag:uefi64,ipxe.efi,,192.168.1.5
dhcp-boot=tag:!ipxe,tag:uefiarm,ipxe-arm64.efi,,192.168.1.5
```

## Step 3 — Drop the Boot Client kernel + initramfs in place

(Skip if you did this under Step 2A.)

iPXE chainloads to AutoDeploy's `http://<host>/ipxe/boot.ipxe`,
which references the kernel + initramfs. Build the initramfs
once:

```sh
sudo /etc/autodeploy/scripts/initramfs/build-initramfs.sh
sudo cp /boot/vmlinuz-$(uname -r) /var/lib/autodeploy/ipxe/autodeploy-kernel
sudo chown autodeploy:autodeploy /var/lib/autodeploy/ipxe/autodeploy-kernel
```

For production, build a small specialised kernel — see the
`scripts/initramfs/` README.

## Step 4 — PXE boot a target machine

Power on. You should see:

```
PXE Boot
Querying DHCP...     [ok]
Loading boot image...
iPXE 1.21.1+ -- Open Source Network Boot Firmware
[for embedded: "Loaded embedded AutoDeploy bootstrap"]
Configuring (net0...) ok
Chainloading http://192.168.20.60:8080/ipxe/boot.ipxe ...
[kernel + initramfs download]
[AutoDeploy boot menu]
```

If you get there, you're done. Move on to [Tutorial 3 — Deploy
your first machine](tutorial-03-first-deploy.md).

## UEFI HTTP Boot (no TFTP, modern firmware only)

UEFI 2.5+ firmware (PCs from ~2017 onwards) can skip TFTP
entirely. Configure DHCP to identify HTTPBoot clients and hand
back an HTTP URL directly:

```
# ISC dhcpd
class "uefi-httpboot" {
    match if substring(option vendor-class-identifier, 0, 10) = "HTTPClient";
    option vendor-class-identifier "HTTPClient";
    filename "http://192.168.1.5:8080/ipxe/boot.ipxe";
}
```

UniFi: not in the UI; use dnsmasq alongside.

## If something doesn't work

See [Troubleshooting → PXE](troubleshooting.md#pxe-boot). The
most common ones:

| Symptom | Probable cause |
|---|---|
| Machine never picks up a boot file | DHCP option 66/67 missing or wrong; firewall blocking TFTP (UDP/69). |
| iPXE loads then loops or sits silent | Two-stage DHCP isn't doing the user-class branch. Switch to embedded iPXE (Step 2A). |
| `iPXE: No more network devices` | NIC driver issue. Swap `ipxe.efi` for `snponly.efi`. |
| TFTP says ENOTFOUND for ipxe.efi | The file isn't in `/var/lib/autodeploy/ipxe/`. Re-run `fetch-ipxe.sh` or `build-embedded-ipxe.sh`. |
| iPXE chainloads but the HTTP fetch fails | Firewall, or HTTPS with a self-signed cert iPXE doesn't trust. Use plain HTTP for the bootfile URL. |
