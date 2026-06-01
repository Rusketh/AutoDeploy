# Windows Updates

AutoDeploy tracks Windows Update KB patches as first-class entities. You define the updates you
care about, upload the `.msu` payloads, then deploy them across your fleet with OS-based targeting
and compliance tracking.

## Update list

![Windows Update list](../images/windowsupdate-list.png)

The list shows each update's **KB number**, **title**, **OS filter**, **severity**, **size**,
**reboot** requirement, and **compliance** status. The compliance column shows how many targeted
machines have the update installed (e.g. "8 / 10 80%"), with a green badge at 100% and amber
otherwise.

## Creating an update

Go to **Updates → New**. Give it a **KB number** (e.g. KB5034441), **title**, optional
**description**, **OS filter** (e.g. "Windows 10" — matches machines whose OS caption contains this
string), and **severity** (critical, important, moderate, low).

![Creating a Windows Update](../images/windowsupdate-new.png)

After saving, upload the `.msu` payload file. The agent downloads and installs it using `wusa.exe`.

## Compliance

Click the **Compliance** button on any update to see a per-machine breakdown of install status:
installed, pending, failed, or unknown.

![Windows Update compliance](../images/windowsupdate-compliance.png)

The compliance view shows summary counts at the top and a per-machine table below, so you can
identify exactly which machines are missing a patch.

## Deploying updates

Click **Deploy updates** to push one or more updates to targeted machines. You can target by
machine name regex, OU, group, specific machine IDs, and OS filter. The deployment creates a
per-machine job for each selected update; the agent picks up queued jobs at its next check-in.

![Deploying Windows Updates](../images/windowsupdate-deploy.png)

## Agent reporting

The agent reports installed KBs on each check-in. The server matches reported KB numbers against
tracked updates to compute compliance. Per-machine KB status is visible on the
[machine detail page](machines.md#history-and-detected-software).

## Next steps

- Review fleet-wide update compliance on the [dashboard](dashboard.md).
- Check per-machine KB status on the [machine detail page](machines.md).
