# BitLocker

AutoDeploy can enable **BitLocker** drive encryption on Windows machines during deployment and
**escrow the recovery keys** centrally, so they're available if a machine ever needs recovery.

BitLocker is a Windows feature; this functionality applies to the Windows
[agent](../introduction.md#agent-autodeploy-agent) on deployed machines.

## How it works

1. You set a **BitLocker PIN** for a machine (or it is configured as part of your process).
2. During deployment, the agent reads its assigned PIN from the server and enables BitLocker.
3. After encryption, the agent **escrows the recovery key** back to the server, where it is
   stored encrypted at rest (see [Security → Secrets at rest](security.md#secrets-at-rest)).
4. Operators can later retrieve the recovery key from the portal when needed.

## PIN format

A BitLocker pre-boot PIN can be either:

- **Numeric** (digits `0`–`9`) — works on every machine with no extra setup.
- **Complex** (also called an *enhanced PIN*) — may include letters, symbols and
  spaces, for example `Bannana10!`. When a machine's assigned PIN contains
  anything other than digits, the agent automatically enables Windows' **"Allow
  enhanced PINs for startup"** policy on that machine before applying it.
  Without that policy, Windows rejects the PIN with *"Your PIN can only contain
  numbers from 0 to 9."*

> **Pre-boot keyboard caveat.** The PIN is typed in the pre-boot environment,
> before Windows loads, where keyboard support is limited. On some firmware the
> keys are mapped as if on a US QWERTY layout (the function keys `F1`–`F10`
> stand in for digits `1`–`0`). Confirm a complex PIN can be entered at boot on
> your hardware before rolling it out widely.

## Managing PINs and keys

Open a machine from **[Machines](../portal/machines.md)** and use its BitLocker panel to:

- **Set the BitLocker PIN** for that machine.
- **View status** — whether a PIN is set.
- **Retrieve the recovery key(s)** escrowed for the machine.

Recovery keys are sensitive: they are encrypted with the server's
[secrets key](security.md#secrets-at-rest), so protecting and backing up that key is essential to
keeping recovery possible.

> Because PINs and recovery keys are protected by the server's secrets key, **losing that key
> makes them unrecoverable.** Make sure it is part of your [backup plan](backup-and-retention.md).

## Related

- [Security](security.md) — how secrets are encrypted at rest.
- [Machines](../portal/machines.md) — where the BitLocker panel lives.
</content>
