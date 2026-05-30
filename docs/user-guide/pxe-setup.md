# PXE setup — does this work with regular PXE?

> **Note:** For a task-oriented PXE walkthrough that covers **UniFi**
> (with the embedded-iPXE build for routers that can't do conditional
> DHCP), dnsmasq, ISC dhcpd and Microsoft DHCP step by step, see
> **[Tutorial 2 — Set up PXE boot](tutorial-02-pxe.md)**. This page is
> the longer-form architecture explanation.

Yes — with a small bridge. AutoDeploy itself speaks **HTTP only**: the
design's "no TFTP, no layer-2 PXE dependency" rule is about how the
**deployment payload** moves. The **bootstrap** (DHCP → first network
boot file → iPXE → AutoDeploy over HTTP) follows the well-established
"chainload iPXE" pattern. AutoDeploy doesn't ship a DHCP or TFTP
server, but any existing one will do.

## The boot chain

There are three ways a machine can pick up AutoDeploy:

```
Mode A — Classic BIOS PXE (the most common)
  1. Machine powers on, firmware does PXE boot via UDP/TFTP.
  2. DHCP server hands back the address of a TFTP server plus the
     filename "undionly.kpxe" (the iPXE chainload binary).
  3. Firmware TFTP-fetches undionly.kpxe and runs it.
  4. iPXE comes up, gets a DHCP lease of its own, asks DHCP for its
     next boot file — DHCP now hands back AutoDeploy's HTTP URL
     (because iPXE identifies itself with a different option-60
     user-class string than the firmware did).
  5. iPXE HTTP-loads autodeploy's /ipxe/boot.ipxe.
  6. boot.ipxe loads the kernel + initramfs over HTTP and boots
     into the AutoDeploy Boot Client.

Mode B — UEFI PXE (modern hardware)
  Same as Mode A, but the firmware loads "ipxe.efi" or
  "snponly.efi" instead of undionly.kpxe.

Mode C — UEFI HTTP Boot (UEFI 2.5+, 2015+ firmware)
  1. Firmware HTTP Boots directly. DHCP hands back a URL pointing
     at /ipxe/boot.ipxe.
  2. Firmware downloads the iPXE script directly over HTTP. No
     TFTP server needed at all.
  iPXE chainload is still required because UEFI HTTP Boot only
  speaks HTTP-to-bootfile, not HTTP-to-kernel-with-cmdline-args;
  iPXE adds the kernel-cmdline plumbing AutoDeploy needs.
```

So **regular PXE works** — you just need iPXE binaries on a TFTP server
that DHCP can point machines at. Modes A and B are by far the most
common; Mode C is convenient when the fleet is uniformly modern UEFI.

## Step 1 — Fetch the iPXE binaries

Run the helper:

```sh
./scripts/fetch-ipxe.sh /var/lib/autodeploy
# downloads to /var/lib/autodeploy/ipxe/:
#   undionly.kpxe     (BIOS PXE chainload)
#   ipxe.pxe          (BIOS PXE chainload, alternative)
#   ipxe.efi          (UEFI x64 chainload)
#   snponly.efi       (UEFI x64 chainload, fallback if NIC driver issues)
#   ipxe-arm64.efi    (UEFI arm64 chainload)
```

The binaries come from boot.ipxe.org. If your environment is air-gapped,
build them yourself from <https://github.com/ipxe/ipxe> and drop them in
the same directory.

These binaries change rarely — typically once or twice a year. Refresh
when you update AutoDeploy.

## Step 2 — Stand up TFTP

AutoDeploy ships an **optional built-in TFTP server**. Enable it by
setting `AUTODEPLOY_TFTP_ADDR=:69` (or any address) and the server
serves `$AUTODEPLOY_DATA_DIR/ipxe/` read-only. Honors the modern PXE
options (`blksize`, `tsize`, `timeout`). No second daemon required.

```sh
AUTODEPLOY_TFTP_ADDR=:69 \
AUTODEPLOY_HTTPS_ADDR=:443 \
AUTODEPLOY_DATA_DIR=/var/lib/autodeploy \
  ./autodeploy-server
# Logs:
#   tftp.listen  addr=:69  root=/var/lib/autodeploy/ipxe
```

Binding to port 69 needs `CAP_NET_BIND_SERVICE` — typically granted
by a systemd unit (`AmbientCapabilities=CAP_NET_BIND_SERVICE`) or
`setcap`. For development, use a high port (`AUTODEPLOY_TFTP_ADDR=:6969`)
and configure DHCP to point at it.

### Or use any existing TFTP daemon

If you already run TFTP for other reasons, point its root at the same
iPXE directory and leave `AUTODEPLOY_TFTP_ADDR` empty.

```sh
# tftpd-hpa (Debian / Ubuntu)
sudo apt install tftpd-hpa
# /etc/default/tftpd-hpa:
TFTP_USERNAME="tftp"
TFTP_DIRECTORY="/var/lib/autodeploy/ipxe"
TFTP_ADDRESS=":69"
TFTP_OPTIONS="--secure"
sudo systemctl restart tftpd-hpa

# dnsmasq (small or single-server environments)
# /etc/dnsmasq.d/autodeploy.conf:
enable-tftp
tftp-root=/var/lib/autodeploy/ipxe
```

Test it:

```sh
tftp <tftp-server>
tftp> get undionly.kpxe
Received 78912 bytes in 0.1 seconds
tftp> quit
```

## Step 3 — Configure DHCP

DHCP detects whether the booting machine is firmware or iPXE (option
60 user-class string) and serves the right boot file. The pattern is
identical across DHCP servers; the syntax differs.

### ISC dhcpd

```
# /etc/dhcp/dhcpd.conf

option arch code 93 = unsigned integer 16;        # client system architecture
option iPXE-encap-opts code 175 = encapsulate iPXE; # iPXE identifies itself in option 175
option user-class code 77 = string;

# The autodeploy host (HTTP).
option autodeploy-url code 252 = text;

class "ipxe-clients" {
    # iPXE sets the "iPXE" user-class string.
    match if substring(option user-class, 0, 4) = "iPXE";
}
class "uefi-x64" {
    match if option arch = 00:07 or option arch = 00:09;
}
class "uefi-arm64" {
    match if option arch = 00:0b;
}

subnet 10.10.10.0 netmask 255.255.255.0 {
    range 10.10.10.100 10.10.10.200;
    option routers 10.10.10.1;
    next-server 10.10.10.5;   # the TFTP server

    # If we're already iPXE, jump straight to AutoDeploy over HTTP.
    if exists user-class and option user-class = "iPXE" {
        filename "http://autodeploy.corp.example/ipxe/boot.ipxe";
    }
    # First boot: hand back the right iPXE binary for the arch.
    elsif option arch = 00:07 or option arch = 00:09 {
        filename "ipxe.efi";              # UEFI x64
    }
    elsif option arch = 00:0b {
        filename "ipxe-arm64.efi";        # UEFI arm64
    }
    else {
        filename "undionly.kpxe";         # legacy BIOS
    }
}
```

The key idea is the `if exists user-class and option user-class = "iPXE"`
branch: the first time DHCP hears from the firmware, it returns the
iPXE binary. The second time (now from iPXE itself), it returns the
HTTP URL.

### Microsoft DHCP Server

Open the DHCP console, right-click the scope → **Configure Options** →
**Advanced** tab:

| Option | Vendor class | Value |
|--------|--------------|-------|
| 060 (Class ID) | Microsoft Windows Options | (set per the scope-options pattern; see Microsoft docs)  |
| 066 (Boot Server Host Name) | Standard | `<TFTP-server-IP>` |
| 067 (Bootfile Name) | Standard | `undionly.kpxe`  |

For iPXE chainload, create a **vendor class** that matches
`iPXE` (user-class option 77) and set Option 067 to
`http://autodeploy.corp.example/ipxe/boot.ipxe` for that class.

Microsoft has a long-standing how-to that is more comprehensive than
this summary: <https://learn.microsoft.com/en-us/windows-server/networking/technologies/dhcp/dhcp-pxe-boot>.

### Kea DHCP

```yaml
# /etc/kea/kea-dhcp4.conf
Dhcp4:
  client-classes:
    - name: "iPXE"
      test: "substring(option[77].hex,0,4) == 'iPXE'"
    - name: "UEFI-x64"
      test: "option[93].hex == 0x0007 or option[93].hex == 0x0009"
    - name: "UEFI-arm64"
      test: "option[93].hex == 0x000b"

  subnet4:
    - subnet: "10.10.10.0/24"
      pools: [{ pool: "10.10.10.100 - 10.10.10.200" }]
      next-server: 10.10.10.5
      boot-file-name: "undionly.kpxe"
      option-data:
        - name: "routers"
          data: "10.10.10.1"
      reservations: []
      # Per-class overrides.
      client-class: ""
      option-data: []
      # iPXE jumps to HTTP.
      pools-defaults:
        boot-file-name: "undionly.kpxe"

# Then add eval rules per class — see Kea docs for the full syntax.
```

Refer to the Kea manual; the principle is identical to ISC dhcpd.

## Step 4 — UEFI HTTP Boot (Mode C, no TFTP)

If your fleet is uniformly UEFI 2.5+ and you'd rather not run a
TFTP server at all:

```
# ISC dhcpd snippet — match on client system architecture for HTTP Boot
class "uefi-httpboot" {
    match if substring(option vendor-class-identifier, 0, 10) = "HTTPClient";
    option vendor-class-identifier "HTTPClient";
    filename "http://autodeploy.corp.example/ipxe/boot.ipxe";
}
```

Firmware that supports HTTP Boot sets a vendor-class-identifier of
`HTTPClient:Arch:00016:UNDI:003016` (or similar). The DHCP server
matches on the `HTTPClient` prefix and returns an HTTP URL directly.

Even in this mode, iPXE chainload is needed: UEFI HTTP Boot fetches the
file at the URL and executes it, but the file format expected is an EFI
binary, not raw kernel+initramfs with cmdline args. So the URL points at
`http://.../ipxe/boot.ipxe`, which the firmware executes via iPXE's
embedded HTTP-aware UEFI loader.

## What AutoDeploy serves

Whichever bootstrap mode you use, the iPXE chainload eventually fetches
this URL from AutoDeploy:

```
GET http://autodeploy.corp.example/ipxe/boot.ipxe
```

The script that comes back hands iPXE the kernel + initramfs URLs and
the kernel command line:

```
#!ipxe
set base http://autodeploy.corp.example
kernel ${base}/ipxe/static/autodeploy-kernel console=tty1 console=ttyS0,115200 autodeploy.server=${base} autodeploy.uuid=${uuid}
initrd ${base}/ipxe/static/autodeploy-initrd
boot
```

The `autodeploy-kernel` and `autodeploy-initrd` are **prebuilt**,
attached to each release and fetched automatically by the installer /
`fetch-ipxe.sh` into `$AUTODEPLOY_DATA_DIR/ipxe/` — no build step.
(Build your own with `scripts/initramfs/build-initramfs.sh` only for
exotic hardware.) From there, AutoDeploy's HTTP-only flow takes over —
menu, manifest, payload download, image apply.

## Troubleshooting

| Symptom                                        | Likely cause                                                                                          |
|------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| Machine never picks up the boot file           | DHCP option 66/67 wrong, or firewall blocking TFTP (UDP/69) between client and TFTP server.            |
| iPXE loads, then errors `No more network devices` | NIC driver issue. Try `snponly.efi` instead of `ipxe.efi` (UEFI), or `ipxe.pxe` instead of `undionly.kpxe` (BIOS). |
| iPXE loads, asks DHCP, gets the firmware boot file again (loops) | DHCP isn't checking the user-class. Both firmware and iPXE are getting the same answer. Fix the class match in DHCP config. |
| iPXE chainloads but the HTTP GET to /ipxe/boot.ipxe fails | TLS issue or firewall. Try plain HTTP first; the script can chainload to HTTPS later via `chain http+://autodeploy/...`. |
| AutoDeploy menu appears but no machines are recognised in inventory | The Boot Client is running fine. Bind the machine via **Machines** in the portal to enable re-imaging. |

## What AutoDeploy does NOT bring

By deliberate design:

- **No DHCP server.** Use your existing DHCP.
- **No multicast.** Payload distribution at scale is solved by
  per-site **payload mirrors** (see [scaling.md](scaling.md)), not
  multicast. Multicast WIM transport is a niche optimisation that
  the design's open question #7 leaves as a future addition if
  unicast + mirrors prove insufficient.

The built-in TFTP server is **optional** — turn it on with
`AUTODEPLOY_TFTP_ADDR=:69` to serve the iPXE bootstrap binaries
without a separate daemon, or leave it off and use your existing TFTP
infrastructure.
