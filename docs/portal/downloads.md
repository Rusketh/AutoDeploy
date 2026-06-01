# Downloads

The **Downloads** page serves binaries to authenticated portal users — most usefully the Windows
[agent](../introduction.md#agent-autodeploy-agent), but also the Boot Client and any installer
scripts you ship alongside the server. The files come from the server's downloads directory; the
install scripts seed it automatically from the release bundle, and you can drop more in yourself.

![Downloads](../images/downloads.png)

## What's listed

Files are grouped by type (agent, Boot Client, server, scripts). Each entry shows its **filename**,
a **platform** label, **size**, **modified** time, and a **Download** link. The page recognises the
standard release filenames so it can group and label them, for example:

- `autodeploy-agent-windows-amd64.exe` — Windows agent (and the `arm64` variant)
- `autodeploy-boot-linux-amd64` — Boot Client (Linux, pre-OS)
- `autodeploy-server-linux-amd64` — server binary

If the directory is empty, the page tells you where to drop files and which names to use.

## When you'd use it

The Windows agent is normally installed automatically during a PXE deploy, so you don't usually
need this page. Download the agent here when you want to bring an existing machine under management,
recover a machine after a restore, or test the agent against a dev portal.

## Related

- [PXE & boot setup](../install/pxe-and-boot.md) — the Boot Client image and the network-boot chain.
- [Settings → PXE](settings.md#pxe) — boot-chain diagnostics and DHCP snippets.
- [Settings → Updates](settings.md#updates) — staging agent binaries for fleet self-update.
