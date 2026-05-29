# Troubleshooting

Symptom → fix. Start here when something's wrong.

## Server / portal

### Browser shows `https://...` even though I configured HTTP

Almost always **browser auto-upgrade**, not the server. Modern
Chrome / Edge / Firefox / Safari silently rewrite `http://` to
`https://` for any non-loopback hostname.

**Confirm the server isn't redirecting:**

```sh
# On the server itself
curl -v http://127.0.0.1:8080/portal/ 2>&1 | head -20

# From the client machine
curl -v http://<server>:8080/portal/ 2>&1 | head -20
```

If `curl` returns a `200`, the server's fine. The browser is the
upgrader.

**Disable the upgrade:**

| Browser | Where |
|---|---|
| Chrome / Edge | Settings → Privacy → Security → "Always use secure connections" → off, OR add an exception for your host |
| Firefox | `about:preferences#privacy` → "HTTPS-Only Mode" → "Don't enable", OR add an exception |
| Safari | No off switch; use `127.0.0.1:8080` (loopback is exempt) or set up HTTPS |

Or test from `127.0.0.1` (loopback is exempt from auto-upgrade).

### Server won't start — `AUTODEPLOY_TLS_CERT and AUTODEPLOY_TLS_KEY must be set in production mode`

You set `AUTODEPLOY_HTTPS_ADDR` but no cert paths, with
`AUTODEPLOY_DEV=false`. Three fixes:

1. **You want HTTP-only**: comment out `AUTODEPLOY_HTTPS_ADDR` in
   `/etc/default/autodeploy`. Restart.
2. **You want HTTPS with a real cert**: fill in
   `AUTODEPLOY_TLS_CERT` and `AUTODEPLOY_TLS_KEY` paths.
3. **You want HTTPS with an auto-generated dev cert**: set
   `AUTODEPLOY_DEV=true`. (Not for production.)

### Server won't start — `permission denied` binding to :443 or :69

The systemd unit needs `AmbientCapabilities=CAP_NET_BIND_SERVICE`.
The installer sets it; check with:

```sh
systemctl cat autodeploy | grep -i cap
# Should show: AmbientCapabilities=CAP_NET_BIND_SERVICE
```

If your distro doesn't honour the ambient-cap directive (some
older Ubuntu), either:

- Switch to a high port: `:8443` or `:6969`, and have your DHCP /
  reverse proxy NAT to it.
- Run `setcap 'cap_net_bind_service=+ep' /usr/local/bin/autodeploy-server`.

### Settings → Updates shows "Update to vX.Y.Z" when I'm running the latest

Two cases:

**You're running a build-from-source ("pre-release build" badge):**

Expected. Builds with a non-semver version like `pr10-f81b5f3`
aren't comparable to tagged releases. The page shows
"pre-release build" warn badge plus an "Install vX.Y.Z" button
that would replace your dev binary with the latest tagged
release (effectively a downgrade if your branch is ahead).

**You ARE on a tagged release that matches:**

This is a bug — file an issue.

### Settings → Updates "Install agent" button does nothing

(Fixed in PR #12 — wrap the script in DOMContentLoaded). If
you're on an older build:

```sh
curl -v -X POST http://<server>:8080/api/v1/server/install-agent \
    -H 'Content-Type: application/json' -d '{}' --cookie-jar -
```

That confirms the endpoint works. If it returns 200 but the
button doesn't, the JS isn't bound — update the server.

### `journalctl` shows `http.cleartext_public_bind` WARN

By design. Your `AUTODEPLOY_HTTP_ADDR` is non-loopback in
production mode. The server permits cleartext HTTP — the WARN
just makes the choice auditable. If you'd rather not see it,
front the bind with a reverse proxy that terminates TLS, or set
HTTP_ADDR back to `127.0.0.1:8080` and add a reverse proxy.

## Software packages

### Detection rule never matches even though the app is installed

Most common causes:

1. **32-bit installer on 64-bit Windows**: keys land under
   `HKLM\SOFTWARE\WOW6432Node\...` not `HKLM\SOFTWARE\...`.
   Update the rule.
2. **`%LOCALAPPDATA%` resolves against SYSTEM**: agent runs as
   SYSTEM by default. Per-user installs aren't in SYSTEM's
   profile. Use a `script` rule with a PowerShell loop over
   user profiles.
3. **File path typo**: spaces, backslashes. The agent logs the
   path it actually checked — find the `package.detection.file`
   log line and verify.

### Install step says `file not found` for a bare filename

The bare filename didn't match any uploaded file. Check:

1. Open the package's **Payload files** card.
2. Copy the filename you actually uploaded using the copy button.
3. Paste it in the step's path field; case must match.

### `{payload}` in a multi-file package

`{payload}` only works when the package has exactly one file.
For multi-file packages, use bare filenames. The agent log shows
`payload.token.ambiguous` when this happens.

### Upload progress bar finishes but nothing is listed

Either:

1. The browser was on an old version of the page that POSTed the
   form on success — the response now goes to the multi-file
   list. Hard-refresh (Ctrl+Shift+R).
2. The filename was rejected by the sanitiser (path separators,
   `..`, > 255 chars). Check the flash message bar at the top
   of the page.

## PXE boot

### Target never picks up a boot file

DHCP option 66/67 wrong, OR firewall blocking TFTP (UDP/69)
between client and server.

**Test from the target's subnet:**

```sh
tftp <autodeploy-ip>
tftp> get ipxe.efi
```

If TFTP times out: firewall.
If TFTP works but the firmware doesn't fetch: DHCP option 66
(server) or 67 (filename) wrong.

### TFTP says `ENOTFOUND` for ipxe.efi or snponly.efi

The x86_64 EFI binaries aren't on disk. Two paths to fix:

**Fetch the prebuilt binaries** (correct as of 2026-05; the
boot.ipxe.org layout shifted from `amd64-efi/` to `x86_64-efi/`):

```sh
sudo curl -sSfL -o /var/lib/autodeploy/ipxe/ipxe.efi    http://boot.ipxe.org/x86_64-efi/ipxe.efi
sudo curl -sSfL -o /var/lib/autodeploy/ipxe/snponly.efi http://boot.ipxe.org/x86_64-efi/snponly.efi
sudo chown autodeploy:autodeploy /var/lib/autodeploy/ipxe/{ipxe.efi,snponly.efi}
```

**Or build them with the AutoDeploy URL embedded** (recommended
for UniFi / OPNsense / consumer routers that can't do conditional
DHCP):

```sh
sudo /etc/autodeploy/scripts/build-embedded-ipxe.sh
```

### iPXE loads but loops or sits silent after DHCP

Your DHCP can't differentiate firmware-PXE clients from iPXE
clients. Both get the same bootfile, iPXE asks again, gets sent
its own filename, infinite loop (or silent stall).

**Fix:** use embedded iPXE — see [tutorial 2 step
2A](tutorial-02-pxe.md#step-2a---embedded-ipxe). Build with
`sudo /etc/autodeploy/scripts/build-embedded-ipxe.sh`. After
that, iPXE ignores DHCP's bootfile entirely and chains to the
embedded URL.

### `iPXE: No more network devices`

NIC driver issue inside iPXE.

| Firmware mode | Try |
|---|---|
| BIOS | Swap `undionly.kpxe` for `ipxe.pxe` in your DHCP filename. |
| UEFI | Swap `ipxe.efi` for `snponly.efi`. `snponly.efi` uses the firmware's UEFI Simple Network Protocol driver instead of iPXE's own — works on more NICs, including Hyper-V's synthetic adapter. |

### iPXE chainloads but the HTTP fetch to `boot.ipxe` fails

Common causes:

1. **TLS**: iPXE doesn't trust your self-signed cert. Use plain
   HTTP for the bootfile URL even if the rest of AutoDeploy is
   HTTPS.
2. **Firewall**: target's subnet can't reach the AutoDeploy host
   on port 8080/80/443. `curl` from a same-subnet machine to
   confirm reachability.
3. **HSTS** in the browser doesn't apply here — iPXE doesn't do
   HSTS.

### Boot menu shows but no images are offered

Your machine isn't matching any image's filters. Check:

1. **Images** in the portal → your image → if the **SMBIOS
   filters** section has entries, the machine's serial /
   manufacturer / model must match.
2. **Inventory → Machines** → if the machine has a binding to a
   different image, that takes priority.

### Windows installs but fails OOBE

Almost always a malformed unattend.

1. **Boot Client → Logs** has the generated unattend XML.
2. Copy it out, validate with `Windows-Setup-Validator` or open
   in Windows System Image Manager.

### Hyper-V Gen2 VM never boots / Default Switch issue

Hyper-V's "Default Switch" uses internal NAT with its own
built-in DHCP — the VM never reaches your UniFi / external DHCP,
so PXE never sees AutoDeploy.

**Fix**: in Hyper-V Manager → Virtual Switch Manager, create an
**External** vSwitch bridged to a physical NIC on the same
subnet as the AutoDeploy server. Then on the VM: Settings →
Network Adapter → switch to that External vSwitch.

## Updates / agent

### Agent on the target says "out of date" but I just pushed a new build

The agent checks for updates after each check-in (default 5
minutes). Wait for the next check-in, or restart the agent
process to force one.

### `gh workflow run release.yml --ref <tag>` returns 404

The tag doesn't exist on origin yet, or the workflow's name
changed. List tags with `gh api /repos/Rusketh/AutoDeploy/tags`.

### Auto-tag workflow created a tag but no release was published

GitHub's loop-protection rule: `GITHUB_TOKEN`-pushed tags don't
trigger other workflows by default. The fix is in PR #5 — auto-tag
explicitly dispatches release.yml after tagging.

If you're on an older AutoDeploy main, run:

```sh
gh workflow run release.yml --ref vX.Y.Z --repo Rusketh/AutoDeploy
```

## Logs / observability

### `journalctl -u autodeploy` is too noisy

Adjust retention or filter by event:

```sh
journalctl -u autodeploy --since "1 hour ago" | grep -E "level.*WARN|ERROR"
journalctl -u autodeploy -g "package.install"   # one event family
```

The portal's **Logs** page has the same data with filter/search
and live tail.

### Boot Client logs aren't showing up in the portal

Boot Client runs in a stripped-down initramfs. It buffers logs
to disk and ships them when the deploy completes. If the deploy
hard-fails before the agent comes up, those logs are lost — the
fallback is the kernel's console output, visible on the target's
screen.

## When all else fails

Run the smoke check on the server:

```sh
curl -s http://127.0.0.1:8080/healthz
# {"status":"ok"}

curl -s http://127.0.0.1:8080/api/v1/version
# {"version":"vX.Y.Z","go_version":"go1.x","goos":"linux","goarch":"amd64"}
```

And check the worklog at
[`docs/design/WORKLOG.md`](../design/WORKLOG.md) for the most
recent behavioural changes — features land there before they
land in this guide.
