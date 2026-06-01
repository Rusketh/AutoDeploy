# Updates

AutoDeploy keeps its three components current: the **server** can update itself in place, and the
**agent** running on deployed machines can self-update to match the server. Both are managed from
**[Settings → Updates](../portal/settings.md#updates)**.

## Updating the server

You have two options:

### From the portal

The Updates page shows the running server version and can drive an in-place upgrade. The Linux
installer sets this up by installing a self-update helper at `/usr/local/sbin/autodeploy-update`
and a narrowly-scoped sudoers rule that lets the `autodeploy` service user run **only** that helper
without a password.

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

## Updating the agent

The server distributes the agent binary from its `downloads/` directory. Each binary is
accompanied by a `.version` and a `.sha256` sidecar:

- the **`.version`** sidecar tells the server which agent version it is serving, so it can decide
  when a deployed agent should update;
- the **`.sha256`** sidecar is verified by the agent before it swaps its own binary, so an update
  is only applied if the download is intact.

When the server advertises a newer agent, resident agents download it, verify the checksum, swap
the binary, and restart — unless launched with `-no-self-update`. The
[Downloads page](../portal/downloads.md) shows the agent binaries the server currently has
available.

## Related

- [Settings → Updates](../portal/settings.md#updates)
- [Installation guide](../install/linux-server.md)
- [Downloads](../portal/downloads.md)
</content>
