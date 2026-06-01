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

Domain join is driven by the [unattend](../portal/payloads.md#unattend-files) used by an image:
the unattend captures the domain and the credentials/OU used to join. Combine an unattend that
joins the domain with the directory connection above for fully automated, domain-joined
deployments.

## Related

- [Settings → Active Directory](../portal/settings.md#active-directory)
- [Payloads → Unattend files](../portal/payloads.md#unattend-files)
- [Bulk operations](../portal/bulk-operations.md) — rename machines across the fleet.
</content>
