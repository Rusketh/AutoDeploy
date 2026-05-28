# Active Directory integration

> **Status.** Phase 10. The server-side Domain Integration Service
> performs delete-and-replace computer-object lifecycle via a service
> account and reconciles group memberships from the binding on each
> deploy. The agent / unattend performs the actual join.

## Configuration

Configure AD integration through the portal:

**Settings → Active Directory**

| Field | What |
|-------|------|
| LDAP URL | e.g. `ldaps://dc.corp.example:636`. LDAPS strongly preferred. Empty disables AD. |
| Service-account bind DN | e.g. `CN=autodeploy,OU=Service Accounts,DC=corp,DC=example`. |
| Service-account password | **Secret.** Stored AES-256-GCM encrypted in the database; never logged. Leave blank when editing to keep the existing value. |
| Search base | AD subtree to operate within, e.g. `DC=corp,DC=example`. |
| Skip TLS verification | Lab DCs with self-signed certs only. |

Click **Test connection** to dial the LDAP server, bind with the
service account and run a quick search. The result appears as a
flash message. Click **Save** to persist; changes take effect on
the **next** manifest request — no server restart needed. Click
**Disable AD** to clear every AD setting (turning AD integration
off without losing the rest of your portal configuration).

The service account needs AD rights to create, delete and modify
computer objects in the target OUs, and to modify group memberships
for the groups your bindings reference. No domain-admin equivalent is
required; least-privilege is recommended.

### Environment-variable seed (one-time, optional)

If you'd rather provision AD via configuration management, set these
in `/etc/default/autodeploy`:

```
AUTODEPLOY_AD_URL=ldaps://dc.corp.example:636
AUTODEPLOY_AD_BIND_DN=CN=autodeploy,OU=Service Accounts,DC=corp,DC=example
AUTODEPLOY_AD_BIND_PASSWORD=…
AUTODEPLOY_AD_SEARCH_BASE=DC=corp,DC=example
AUTODEPLOY_AD_SKIP_TLS_VERIFY=false
```

These values are read **only on first start** and only when the
corresponding portal setting is empty. Once you save anything
through the portal, the portal becomes the source of truth and
later changes to the env file are ignored — this avoids the failure
mode where two sources of truth silently drift apart.

## When AD runs

The Domain Integration Service is invoked from the manifest endpoint:

```
POST /api/v1/images/{id}/manifest    (with identity body)
```

When the resolved unattend has a `domain_join` section AND the machine
identified by `system_uuid` has a binding with `machine_name` and
`target_ou`, the server:

1. Searches the AD subtree for an existing computer object at
   `CN=<machine_name>,...`.
2. If found, **deletes it**. (Delete-and-replace lifecycle: this is a
   deliberate, design-level choice. The replacement object gets a new
   SID and GUID; LAPS credentials on the old object are lost; group
   memberships are re-applied below.)
3. Creates a fresh computer object at the binding's `target_ou`.
4. Reconciles group memberships: removes the new object from any
   groups it ended up in but should not be in, adds it to the groups
   listed in `binding.group_memberships`.

Then the manifest returns to the Boot Client, the image is applied,
the unattend runs, and Windows joins the (now-prepared) domain.

## Why delete-and-replace

The design accepts the trade-off explicitly:

- **Lost**: LAPS state stored against the old object's GUID.
- **Lost**: any custom AD attributes set on the old object outside
  AutoDeploy.
- **Preserved**: BitLocker recovery (AutoDeploy escrows recovery keys
  into its own inventory, keyed on hardware identity, not on the AD
  object — Phase 12).
- **Preserved**: group memberships, because they are re-applied from
  the binding rather than copied from the old object.

If group membership reconciliation fails partway, the object exists
but groups are out of date. The server logs the failure loudly; the
operator can fix groups via the portal.

## Logs

Every AD action emits a structured event:

```
addomain.prepare.start            actor=server  target=LAB-01  ou=OU=Lab,...
addomain.prepare.delete_existing  actor=server  target=CN=LAB-01,...
addomain.prepare.ok               actor=server  target=CN=LAB-01,OU=NewLab,...  groups=2
```

The service account password is **never** included in any log line —
not on bind, not on failure. Only the fact that AD operations
happened, and who or what triggered them.
