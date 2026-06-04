# Settings

The **Settings** area is a hub of small pages, each editing one slice of server configuration.
Operator-changeable configuration lives here; bootstrap defaults (data directory, dev mode, the
at-rest secrets key) come from environment variables, while network and listen settings can be
overridden in the portal and take effect on next restart. This page documents each area:

- [Accounts](#accounts)
- [Access PIN](#access-pin)
- [Notifications](#notifications)
- [Branding](#branding)
- [Active Directory](#active-directory)
- [Network](#network)
- [Operational](#operational)
- [Storage](#storage)
- [Updates](#updates)
- [PXE](#pxe)

![Settings hub](../images/settings-index.png)

## Accounts

Local logins for the portal and API. There is no graded permission model — every active account has
full access; accountability rests on the [audit log](logs.md).

![Accounts](../images/settings-accounts.png)

The table lists each account with its **status** (active or disabled) and creation time. For each
account you can **set a new password**, **disable** or **enable** it, or **delete** it (you can't
delete your own account). There's a dedicated field to change your own password, and an **Add
account** form that takes a **username** and **password** (both required).

> The first account (`admin`) is created automatically on first start, with a one-time password
> written to a bootstrap file. Change it here and delete the bootstrap file — see
> [Installing the server](../install/linux-server.md#step-6--first-login).

## Access PIN

An optional **PIN** that gates the PXE boot menu, so only people who know it can start a deployment
from a booting machine.

![Access PIN](../images/settings-access-pin.png)

Type a PIN and **Save** to require it; the Boot Client prompts for it before showing the menu.
Leave the field **empty** to remove the PIN. The PIN is hashed at rest and rate-limited per machine
(repeated failures are locked out).

> Server-initiated re-images (for example, a [bulk re-image](bulk-operations.md)) are already
> authorised by an operator and don't prompt for the PIN at the machine.

## Notifications

A three-channel notification system that fires when significant events occur across the deployment
lifecycle. Operators configure which events trigger notifications, who receives them, and through
which channels. For the full guide, see [Notifications](notifications.md).

![Notification settings](../images/settings-notifications.png)

The settings page is split into three areas:

| Area | Description |
|------|-------------|
| **In-portal** | Master toggle and retention period for the bell-icon notifications |
| **Email (SMTP)** | SMTP host, port, credentials, TLS mode, from address; a **Send test** button verifies the config |
| **Webhooks** | List of configured webhook endpoints with add / edit / test / delivery-log actions |

Each user can also configure per-event preferences (which events → which channels) from
**My preferences**. See [Notification preferences](notifications.md#per-user-preferences) for details.

## Branding

Customise the names, colour, and support details shown in the portal and on the boot menu, plus the
OEM info written to deployed machines.

![Branding](../images/settings-branding.png)

| Field | Notes |
|-------|-------|
| Product name | Shown in the portal header and the boot menu (required) |
| Organisation name | Shown alongside the product name |
| Support URL / Support phone | Contact details |
| OEM Manufacturer | Written to the deployed machine's OEM information by the agent (defaults to the organisation name) |
| Primary colour | Accent colour for buttons, badges, and the active nav highlight |
| Logo | A data URL, or upload an image to embed it |

## Active Directory

LDAP connection details so AutoDeploy can manage computer objects for
[unattend files](payloads.md#unattend-files) that join a domain.

![Active Directory](../images/settings-active-directory.png)

| Field | Example |
|-------|---------|
| LDAP URL | `ldaps://dc.corp.example:636` |
| Service-account bind DN | `CN=autodeploy,OU=Service Accounts,DC=corp,DC=example` |
| Service-account password | Stored encrypted at rest; never logged |
| Search base | `DC=corp,DC=example` |
| Skip TLS verification | Lab DCs with self-signed certs only |

A **Test connection** button validates the settings, and there's a **Disable AD** action. Changes
take effect on the next manifest request — no restart needed. For the full setup and workflow, see
the [Active Directory operations guide](../operations/active-directory.md).

## Network

Listen addresses, TLS certificates, reverse proxy support, and the external URL for remote agents.

![Network settings](../images/settings-network.png)

### Listen addresses

| Field | Example | Notes |
|-------|---------|-------|
| HTTP address | `0.0.0.0:8080` | Host and port for cleartext HTTP. `127.0.0.1:8080` restricts to loopback; `0.0.0.0:8080` listens on all interfaces. Empty disables HTTP |
| HTTPS address | `0.0.0.0:8443` | Host and port for HTTPS. Empty disables HTTPS. When enabled without a certificate, a self-signed cert is auto-generated |

Both fields show the current environment default for reference. Changes take effect on next server
restart.

### External URL

The public address that remote agents use to reach this server from outside the local network — for
example, through a firewall or reverse proxy. Agents provisioned after this is set receive the URL
in their `/api/v1/agent/self` response and can use it to reconnect.

Leave empty if all agents are on the same LAN as the server.

### Reverse proxy

| Field | Notes |
|-------|-------|
| Trusted proxy CIDRs | Comma-separated list of IP ranges (e.g. `10.0.0.0/8, 172.16.0.0/12`) whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are trusted for client IP detection. Leave empty to trust no proxies |
| External access | **Full access** (default) allows all traffic through the proxy. **Agent only** restricts proxied requests to agent endpoints (`/api/v1/agent/*`, `/payload/*`, `/healthz`) — the portal and admin API are blocked for external traffic, so operators must access them on the internal network |

Both settings take effect immediately — no restart needed.

> **Agent only mode** is recommended for production deployments where agents connect remotely
> through a public reverse proxy but the portal should remain internal. This prevents external
> users from reaching the login page, admin API, or any operator-facing endpoint.

### TLS certificate

You can specify TLS certificate and key paths directly (for certificates managed outside AutoDeploy),
or **upload** PEM-encoded files through the portal. Uploaded files are stored in the server's data
directory under `tls/` with restricted permissions (0600).

When an HTTPS address is configured but no certificate is provided, the server auto-generates a
self-signed P-256 certificate covering `localhost` and `127.0.0.1`. The certificate info badge
shows the current cert's CN, SANs, and expiry.

> **Production deployments** should use a CA-signed certificate, or front the server with a reverse
> proxy (e.g. nginx, Caddy, Traefik) that terminates TLS and forwards to a loopback HTTP listener.

## Operational

Log retention and payload concurrency.

![Operational settings](../images/settings-operational.png)

| Field | Notes |
|-------|-------|
| Log retention (days) | How long [activity log](logs.md) entries are kept; `0` disables pruning |
| Maximum concurrent `/payload/*` streams | Caps simultaneous payload downloads so a rush queues instead of thrashing the server (default 128); excess requests queue for up to 2 minutes. `0` = unlimited. Takes effect on next restart |

## Storage

Where each category of payload is stored on disk. The relative paths in the database stay the same;
the storage layer routes each category to the configured root. Empty = the default
`$AUTODEPLOY_DATA_DIR/<category>`.

![Storage settings](../images/settings-storage.png)

The categories are **ISO**, **drivers**, **software**, **iPXE** (boot files — see
[PXE & boot](../install/pxe-and-boot.md)), and **downloads** (the [Downloads](downloads.md) page).
For each, the page shows the effective path and whether it's writable.

> **Files are not moved automatically.** Move the existing tree to the new location *before* you
> save, or the server will look in the new directory and find nothing. The page includes a
> step-by-step relocation guide.

## Updates

Keep the server up to date and manage the agent binaries served to your fleet.

![Updates](../images/settings-updates.png)

The **Server** section shows the **running** version against the **latest published** release and
flags whether you're up to date, behind, ahead, or on a pre-release build, with a link to the
release notes. When the in-place updater is installed, an **Update** button upgrades the server;
otherwise the page explains how to set up the updater (the installer does this automatically — see
[Installing the server](../install/linux-server.md)). If an update fails, the page shows a link to
the **update log** for diagnostics.

The **Agents** section lets you install or refresh agent binaries for a chosen OS/arch and lists
the staged binaries with their versions and hashes. Resident agents check for a newer version on
each check-in and self-update. For the full process, see the
[Updates operations guide](../operations/updates.md).

## PXE

A setup-and-diagnostics hub for the network-boot chain.

![PXE diagnostics](../images/settings-pxe.png)

It shows:

- **iPXE binaries** currently served from the iPXE directory (filename, purpose, size, SHA-256,
  Secure Boot role, last modified) with per-file download buttons, and a warning if the built-in
  TFTP listener isn't configured. The binaries are the iPXE project's official signed builds,
  including the Microsoft-signed Secure Boot shim (`ipxe-shim.efi`).
- **DHCP configuration snippets** for common platforms (UniFi, dnsmasq, ISC dhcpd, Microsoft DHCP,
  OPNsense/pfSense), each pre-filled with this server's IP and copy-to-clipboard ready.
- A **verification** recipe to confirm the chain works end-to-end.

See [PXE & boot setup](../install/pxe-and-boot.md) for the full picture.
