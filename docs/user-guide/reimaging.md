# Re-imaging

> **Status.** Phase 9. A machine that is known to inventory and has a
> binding (Phase 8) is offered a **Re-image** option in the Boot Client
> menu. Choosing it rebuilds the machine to the **latest** definition of
> its bound configuration — current ISO, current unattend, drivers that
> match the hardware **today**, current state of the assigned loadout
> and direct package links. Identity (machine name, OU, group
> memberships, software assignment) is preserved.

## The flow

1. The machine PXE-boots into AutoDeploy.
2. The Boot Client reports SMBIOS identity to
   `POST /api/v1/clients/menu`. The server upserts the machine record
   and, finding a binding with `image_id` set, returns a `reimage`
   menu item that points at the bound image.
3. The operator selects the **Re-image** option. The Boot Client runs
   `autodeploy-boot deploy <image-id>` against the bound image.
4. The deploy proceeds exactly like a fresh deploy: the manifest is
   built against the **current** definitions; drivers are matched
   against the reported hardware; software comes from the current
   loadout chain and direct image links.
5. The agent reports the deployment outcome; the machine record's
   `deployment_history` gains a new dated row. The binding (image,
   name, OU, group memberships) is **unchanged**.

## Crucially, this is "latest", not "as it was"

The design is explicit and important: re-imaging is to the **current**
definition of the bound configuration, never to a frozen snapshot of
what was deployed before. If the operator has updated the ISO, swapped
the unattend, added drivers or extended the loadout since the original
deploy, those changes apply.

If a forensic restore of an exact past build is needed, that lives on
top of `deployment_history` as a future capability — not the core
re-image path. Section 9.2 of the design document spells this out.

## What is preserved across re-image

- **Identity**: `machine_name`, `target_ou`, `group_memberships`.
  These come from the binding, not the OS install.
- **Assignment**: the same image, the same loadout. If you want to
  change them, edit the binding.
- **BitLocker PIN** (Phase 12): the operator-set pre-boot PIN is
  re-applied to the new C:.
- **BitLocker recovery-key history** (Phase 12): never overwritten —
  every encryption emits a new recovery key and the old keys stay
  available to unlock historical drives or images.

## What is NOT preserved

- The applied disk contents (the entire C: volume is wiped).
- Any local user-account data or files outside what the configuration
  reinstalls.
- The AD computer object's SID and GUID (Phase 10 delete-and-replace
  lifecycle; LAPS credentials stored against the old object are gone
  with it, and group memberships are re-applied from the binding).
- The volume's encryption key and the current recovery key — the
  re-imaged volume is freshly encrypted (Phase 12), with the same PIN
  but a new recovery key escrowed to inventory.

## Triggering a re-image

The interactive path is the menu option above. There is no separate
HTTP "re-image now" command — that would require the machine to be
booted into the AutoDeploy environment, which only happens on PXE
boot. Operators with deskside access reboot and pick from the menu;
remote operators rely on the operator-side power-on workflow.

A future enhancement could mark a machine in inventory as "next-boot
re-image", causing the Boot Client to skip the menu and proceed
directly; this is intentionally not in Phase 9 because it would let an
operator schedule a destructive operation without a deskside confirm.
