# Active Directory

AutoDeploy can integrate with **Active Directory** so deployed machines join your domain and so
the server can look machines up in the directory. Connection settings are configured under
**[Settings → Active Directory](../portal/settings.md#active-directory)**.

## Connection settings

The directory connection uses these settings (configurable in the portal; they can also be seeded
once from the environment — see the [configuration reference](../reference/configuration.md)):

| Setting | Environment variable | Example |
|---------|----------------------|---------|
| LDAP URL | `AUTODEPLOY_AD_URL` | `ldaps://dc.corp.example:636` |
| Bind DN | `AUTODEPLOY_AD_BIND_DN` | `CN=autodeploy,OU=Service Accounts,DC=corp,DC=example` |
| Bind password | `AUTODEPLOY_AD_BIND_PASSWORD` | *(service account password)* |
| Search base | `AUTODEPLOY_AD_SEARCH_BASE` | `DC=corp,DC=example` |
| Skip TLS verification | `AUTODEPLOY_AD_SKIP_TLS_VERIFY` | `false` |

Use `ldaps://` (LDAP over TLS) wherever possible. `AUTODEPLOY_AD_SKIP_TLS_VERIFY` should only be
enabled for testing against a directory with an untrusted certificate.

> Settings saved in the portal take precedence over the environment values, which are only read
> once on first start to seed the portal configuration.

## Domain join during deployment

There are two ways to join deployed machines to the domain. **Agent-driven join (recommended)** is
configured per image; the legacy **unattend join** is kept for backwards compatibility.

### Agent-driven join (recommended)

Configure the join on the **image** itself, under the *Active Directory domain join (via agent)*
section of the [image editor](../portal/images.md#active-directory-domain-join). The deployed
agent performs the join **after first boot**, once Windows is fully up with working networking and
DNS — which is far more reliable than joining mid-Setup.

| Field | Notes |
|-------|-------|
| Enable | Turns on agent-driven join for machines built from this image |
| Domain (FQDN) | e.g. `corp.example.com` |
| Computer object OU | Optional DN, e.g. `OU=Workstations,DC=corp,DC=example`. A machine's [binding](../portal/machines.md#bindings) **Target OU** overrides this per machine. |
| Join account | A **least-privilege** account allowed to join computers to the domain |
| Join account password | Stored **encrypted at rest** and handed only to the deploying agent — it is **never written into the unattend XML** |

How it works:

1. After first boot the agent asks the server whether its image is set to join, and for the
   credentials.
2. If it isn't already a member of the target domain, it runs the join and reboots to complete it.
   The credentials are passed to Windows in memory only and are never logged.
3. Because the image joins via the agent, AutoDeploy **automatically suppresses** the unattend's
   own domain-join block for that image, so Setup never attempts (and stalls on) an online join.

> The agent join needs the directory to be reachable from the deployed machine (correct DNS to a
> domain controller). The server's LDAP connection above is not required for the join itself, but
> is used for related directory lookups and [bulk rename](../portal/bulk-operations.md)
> coordination.

### Legacy: unattend join

An [unattend](../portal/payloads.md#unattend-files) can also carry the domain, credentials and OU
directly, so Windows Setup joins during the *specialize* pass. This requires a domain controller to
be reachable mid-Setup and is less reliable — see
[Troubleshooting](troubleshooting.md#a-machine-doesnt-join-the-domain). These fields are **ignored**
when the image is configured for agent-driven join.

## Related

- [Images → Active Directory domain join](../portal/images.md#active-directory-domain-join)

- [Settings → Active Directory](../portal/settings.md#active-directory)
- [Payloads → Unattend files](../portal/payloads.md#unattend-files)
- [Bulk operations](../portal/bulk-operations.md) — rename machines across the fleet.
</content>
