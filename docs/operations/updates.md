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
2. adding a narrowly-scoped sudoers rule at `/etc/sudoers.d/autodeploy-update` that lets the
   `autodeploy` service user run **only** that helper without a password.

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

## Upgrading from an earlier release

This release is a drop-in upgrade: stop the service, replace the binary (or use either method
above), start it again. Your data directory, sessions, agents and boot media keep working. What you
will notice, and what to check:

**What changes for operators**

- **Grouped navigation.** The top bar has four menus — **Fleet**, **Build**, **Patching**,
  **System** — instead of one long row ([details](../portal/dashboard.md#finding-your-way-around)).
- **Names instead of IDs** everywhere (machines, images, deployments), readable times
  ("5m ago", with the exact time on hover) and sizes.
- **Search buttons.** List searches run when you press **Search** or Enter, on the server, across
  every page; each search is a page address you can bookmark. Old `?q=` bookmarks still work.
- **Logs page** rewritten: plain-English rows linked to the machine and the deployment run, one
  search form (text, level, category, machine, user, group, time window, run), an audit trail of
  portal changes, and per-machine / per-run links from the machine page
  ([details](../portal/logs.md)).
- **Machines**: a status for every machine, status chips and filters, a *Current state* panel on the
  machine page that shows the first error and what to try, and a *Needs attention* panel on the
  dashboard.
- **Bulk operations have a review step.** **Review operation** shows what will happen, to which
  machines, before anything is queued; a re-image asks you to type the number of machines
  ([details](../portal/bulk-operations.md#6-review-and-queue)).
- **Settings hub** cards show each area's current values and warnings.
- **Sessions slide** instead of expiring 12 hours after sign-in, with a **Stay signed in** prompt;
  a lapsed session returns you to the page you were on.
- **ISO export**: write the exported ISO with **Rufus in ISO mode** (GPT / UEFI); `dd`-style writers
  won't produce a bootable stick. Exports are built atomically, can be cancelled, and refuse to
  start when the image's edition isn't on the ISO ([details](../portal/images.md)).
- **Boot client** shows the machine's identity, *Step N of M* and a red failure screen with an
  error code and Retry / Restart / Shell keys
  ([details](../install/pxe-and-boot.md#what-the-imaging-screens-show)).

**Compatibility notes**

- **Database migration `0035`** runs automatically on first start. It only *adds* nullable columns
  and indexes to the log table — nothing is rewritten or removed, existing log rows display with
  the new wording, and an older server can still open the database if you have to roll back.
- **New, optional environment variables:** `AUTODEPLOY_SESSION_IDLE_TIMEOUT` (default `12h`) and
  `AUTODEPLOY_SESSION_MAX_LIFETIME` (default `168h`) — see
  [Configuration](../reference/configuration.md). Sessions signed in before the upgrade stay valid.
- **Rejected form saves return HTTP `422`** and re-show the form with your values and the problem
  marked, instead of redirecting to a blank form. Scripts that drive portal forms and check for a
  `302` on failure should check the status code.
- **Portal background requests with an expired session get `401`** (instead of an HTML sign-in
  page with `200`). The JSON API under `/api/v1` is unchanged.
- **The bulk form now has a review step.** Posting the old form directly (scripts, an old open tab)
  still queues the operation as before; the review and the typed re-image confirmation are part of
  the portal's own flow.
- **Settings pages may now refuse values they used to accept** — an unknown time zone, a malformed
  listen address, external URL or proxy range, out-of-range numbers. Values already saved are kept
  and shown; you only meet the check when you next save that page.
- **Exported ISO answer files use a disk guard**: before partitioning, a check on the stick refuses
  to continue unless the PC booted in **UEFI** mode and disk 0 is an internal disk that is not the
  USB stick. Such a PC powers off with a message instead of wiping the wrong disk. Re-export images
  you deploy by USB; network (PXE) deploys are unchanged.
- **Agents and boot clients**: older agents and boot clients in the field keep working against this
  server, and the new ones keep working against an older server until you upgrade it (they only add
  optional log fields and a header). Update them at your own pace.

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
