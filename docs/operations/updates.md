# Updates

AutoDeploy keeps its three components current: the **server** can update itself in place, and the
**agent** running on deployed machines can self-update to match the server. Both are managed from
**[Settings → Updates](../portal/settings.md#updates)**.

## Updating the server

You have two options:

### From the portal

The Updates page shows the running server version and can drive an in-place upgrade. The Linux
installer sets this up by:

1. installing the self-update helper script at `/usr/local/sbin/autodeploy-update`,
2. adding a narrowly-scoped sudoers rule at `/etc/sudoers.d/autodeploy` that lets the `autodeploy`
   service user run **only** that helper without a password.

Before triggering the update the server verifies the sudoers entry is in place. If it is missing
(for example, after a manual install that skipped the installer), the Updates page shows a
diagnostic message explaining how to set it up.

The update process writes progress to a log file (`update.log` in the data directory). If an update
fails, you can inspect the log from **Settings → Updates** (the page shows a link on failure) or
via the `GET /api/v1/server/update-log` [API endpoint](../reference/api.md#version--server-update).

### Manually

Re-run the install from a newer release:

```bash
TAG=$(curl -fsSL https://api.github.com/repos/Rusketh/AutoDeploy/releases/latest \
  | grep -oP '"tag_name":\s*"\K[^"]+')
BASE="https://github.com/Rusketh/AutoDeploy/releases/download/$TAG"
curl -fLO "$BASE/autodeploy-server-linux-amd64"
curl -fLO "$BASE/autodeploy-extras.tar.gz"
tar xzf autodeploy-extras.tar.gz
sudo ./scripts/install-linux.sh
sudo systemctl restart autodeploy
```

The installer upgrades the binary in place and preserves your data directory and database. See the
[installation guide](../install/linux-server.md) for details.

Check the running version any time with:

```bash
autodeploy-server --version
```

### What the update script does

The update script (`/usr/local/sbin/autodeploy-update`) downloads the latest release from GitHub,
replaces the server binary, fetches agent and boot-client binaries for **all available
OS/architecture combinations** from the release (not just a single platform), and restarts the
service. If the GitHub API is unreachable it falls back to fetching the legacy single-agent binary.

## Updating the agent

The server distributes agent binaries from its `downloads/` directory. Each binary is
accompanied by a `.version` and a `.sha256` sidecar:

- the **`.version`** sidecar tells the server which agent version it is serving, so it can decide
  when a deployed agent should update;
- the **`.sha256`** sidecar is verified by the agent before it swaps its own binary, so an update
  is only applied if the download is intact.

When the server advertises a newer agent, resident agents download it, verify the checksum, swap
the binary, and restart — unless launched with `-no-self-update`. The
[Downloads page](../portal/downloads.md) shows the agent binaries the server currently has
available.

### Automatic agent provisioning

Both the installer (`install-linux.sh`) and the update script automatically discover and download
agent and boot-client binaries for every platform published in the matching GitHub release. You no
longer need to manually fetch agent binaries from the Downloads page after installation or an
update — they are seeded automatically.

## Related

- [Settings → Updates](../portal/settings.md#updates)
- [Installation guide](../install/linux-server.md)
- [Downloads](../portal/downloads.md)
</content>
