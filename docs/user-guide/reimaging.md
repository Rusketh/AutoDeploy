# Re-imaging

> Rebuilds a known machine to the **current** definition of its
> bound configuration. Identity is preserved; the disk is wiped.

## The flow

1. The machine PXE-boots into AutoDeploy.
2. The Boot Client reports SMBIOS identity. The server finds a
   binding with `image_id` set and returns a **Re-image** menu
   item that points at the bound image.
3. The operator selects **Re-image**. The Boot Client runs
   `autodeploy-boot deploy <image-id>` against the bound image.
4. The deploy proceeds exactly like a fresh deploy — the manifest
   is built against the **current** definitions; drivers match
   against the reported hardware; software comes from the current
   loadout chain and direct image links.
5. The agent reports the deployment outcome; the machine record's
   `deployment_history` gains a new dated row. The binding (image,
   name, OU, group memberships) is **unchanged**.

## Crucially: latest, not as-it-was

The design is explicit: re-imaging is to the **current** definition
of the bound configuration, never to a frozen snapshot. If the
operator has updated the ISO, swapped the unattend, added drivers,
or extended the loadout since the original deploy, those changes
apply.

Forensic restore of an exact past build is a possible future
feature on top of `deployment_history`, but it's not the core
re-image path.

## What is preserved across re-image

| Thing | How |
|---|---|
| **Machine name** | From the binding. Layered into the unattend's `<ComputerName>` at deploy. |
| **Target OU** | From the binding. Layered into `<MachineObjectOU>` when domain join is configured. |
| **AD group memberships** | From the binding. The Domain Integration Service re-applies them after recreating the computer object. |
| **BitLocker PIN** | The operator-set pre-boot PIN is re-applied to the new C:. |
| **Recovery-key history** | Never overwritten — every encryption emits a new key, the old ones stay available. |
| **Inventory record** | The `machine_record` row keeps the same id; first-seen is preserved; last-seen updates. |
| **Deploy token mechanism** | The agent receives a fresh token on its open report. |

## What is NOT preserved

- The applied disk contents (entire C: volume is wiped).
- Any local user-account data or files outside what the
  configuration reinstalls.
- The AD computer object's SID and GUID (delete-and-replace
  lifecycle; LAPS credentials stored against the old object are
  gone with it).
- The volume's encryption key and the current recovery key — the
  re-imaged volume is freshly encrypted with the same PIN but a
  new recovery key escrowed to inventory.

## Per-machine identity in the unattend

The Boot Client passes the SMBIOS UUID through the unattend URL the
manifest hands it. The server looks up the binding and overrides:

- `NameStrategy=literal` + `ComputerName=<binding name>`
- `DomainJoin.OU=<binding OU>` (when domain join is configured)

That's what makes the re-imaged machine come up with the same name
the operator originally bound it under, and what makes the joined
AD name match the prepared object.

## Triggering a re-image

The interactive path is the menu option above. There is no separate
HTTP "re-image now" command — that would require the machine to be
booted into the AutoDeploy environment, which only happens on PXE
boot.

Operators with deskside access reboot and pick from the menu;
remote operators rely on the operator-side power-on workflow (Wake
on LAN, KVM, IPMI).

A future "next-boot re-image" flag would let an operator schedule a
destructive operation without a deskside confirm; this is
intentionally not yet implemented because it removes a safety step.

## API

There is no dedicated re-image endpoint — the menu endpoint returns
a `reimage` item automatically when a binding exists with an
`image_id`. The Boot Client picks it up in the boot menu.

```sh
# What the Boot Client sees when it requests the menu for a known
# machine (binding with image_id 7):
POST /api/v1/clients/menu
{"system_uuid":"<uuid>"}

# Response includes a reimage item:
{
  "items": [...],
  "reimage": {"image_id": 7, "name": "Office Workstation (Win11)"}
}
```
