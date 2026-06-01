# Mirrors

A **mirror** is a site-local cache of payloads. The bandwidth-heavy part of a deployment is
streaming Windows media to each machine; for a remote site or a large batch, having every machine
pull that from the central server can saturate a WAN link. A mirror serves payload data close to
the machines instead.

![Mirror list](../images/mirrors-list.png)

The list shows each mirror's **name**, **site**, **base URL**, and **health**.

## Adding a mirror

Go to **Mirrors → New** and fill in:

| Field | Notes |
|-------|-------|
| Name | Required — a label for the mirror |
| Description | Optional |
| Base URL | Required — where the mirror serves the `/payload/...` tree, e.g. `https://mirror-eu.corp.example` |
| Site | The site this mirror serves (e.g. `eu-west`, `london-1`). Empty = a global mirror used when no site-specific one matches |
| Priority | Lower wins among same-site mirrors (default 100) |

Once a mirror exists, you can mark it **healthy** or out-of-service from its edit page.

![Creating a mirror](../images/mirror-new.png)

The mirror host can be any HTTP server that serves the same `/payload/...` bytes as the primary —
for example an nginx/Apache reverse proxy in front of an rsync'd copy of the data directory, a
pull-through cache, or a second AutoDeploy server pointed at a synced data dir. The edit page
includes a setup checklist.

## How clients route to a mirror

The [Boot Client](../introduction.md#boot-client-autodeploy-boot) carries a **site** value, set
with its `-site` flag or via `autodeploy.site=<name>` on the kernel command line (which DHCP option
175 or the iPXE chainload script can supply). The client passes its site to the server (in the
`X-AutoDeploy-Site` header), and the server routes that client's payload downloads to a matching
enabled mirror — preferring the lowest-priority same-site mirror, then a global mirror, then the
central server.

So the flow is:

1. You stand up a mirror at a remote site and register it here with that site's name.
2. Machines at that site boot with the matching site value.
3. Their payload downloads are served from the local mirror instead of the central server.

Machines with no site, or no matching healthy mirror, download from the central server as usual.

## Next steps

- For the full topology, throttling, and capacity guidance, see
  [Scaling](../operations/scaling.md).
