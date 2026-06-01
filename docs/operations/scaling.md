# Scaling

AutoDeploy is built to deploy many machines at once. This page covers the controls that keep large
fleets and multi-site deployments healthy.

## Payload mirrors

A deployment streams gigabytes of install media to each machine. When you deploy at scale — or to
remote sites — route that traffic through **[mirrors](../portal/mirrors.md)**: site-local caches
that serve the same payload tree as the central server.

- Create mirrors under **[Mirrors](../portal/mirrors.md)**, each with a `base_url` and optional
  `site`.
- The [boot client](../introduction.md#boot-client-autodeploy-boot) advertises its site (via the
  `-site` flag or the `autodeploy.site=` kernel parameter), and the server hands back payload URLs
  pointing at the best mirror for that site, falling back to a global mirror, then the central
  server.

## Payload throttling

The server bounds how many payload streams run concurrently with
`AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT` (default **128**). This protects the server from exhausting file
descriptors when a large batch of machines all start pulling media at once.

When all slots are in use, additional requests queue for up to **2 minutes**. If a slot doesn't
free up in time the request fails with a timeout, so machines retry on their next cycle rather than
piling up indefinitely.

- Raise the limit on a powerful server with fast storage and network.
- **Do not set it to `0` (unlimited)** on a production node — a large simultaneous PXE burst can
  exhaust resources.

This value is configurable under [Settings → Operational](../portal/settings.md#operational) and
documented in the [configuration reference](../reference/configuration.md).

## Bulk operations

To act on many existing machines at once — reimage, push software, rename, or run a script — use
**[bulk operations](../portal/bulk-operations.md)** rather than touching machines one by one.

## Related

- [Mirrors](../portal/mirrors.md)
- [Configuration reference](../reference/configuration.md)
- [Bulk operations](../portal/bulk-operations.md)
</content>
