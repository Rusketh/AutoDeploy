# AutoDeploy Setup-Lock Credential Provider

A native Windows **Credential Provider** (COM in-proc DLL) that shows a branded,
full-screen "setup" screen **instead of the Windows logon screen** while the
AutoDeploy agent performs a machine's *initial* software rollout — blocking
sign-in until the machine is ready for first use.

This is the C++ half of the `setup_lock` per-image feature. The Go server, boot
client and agent handle the rest (see the repo-level change set). The operator
never builds or installs this DLL by hand: CI builds it and it ships through the
agent's existing zero-touch distribution channel.

## Behaviour

The DLL keys all behaviour off an on-disk **lock marker** and state directory
that the agent and `SetupComplete.cmd` agree on:

```
%ProgramData%\AutoDeploy\lock\
  active         present => lock armed (cover the logon screen)
  status.json    {phase, activity, done, total, percent}  (live progress)
  branding.json  {product_name, organisation_name, primary_color, ...}
  pin-request    written by this DLL  -> the entered technician PIN
  pin-response   written by the agent -> "allow" / "deny"
```

- **Marker present (initial deployment):**
  - `ICredentialProviderFilter` hides **every other** credential provider, so a
    regular user cannot sign in.
  - A branded tile shows the live activity + progress from `status.json`,
    refreshed on a background thread.
  - A branded full-screen window is painted on every **secondary** monitor
    (`fullscreen.cpp`); the **primary** monitor shows the interactive branded
    credential tile (so the unlock link stays clickable — a full-screen window
    over it would block all input).
- **Marker absent (machine past its initial rollout):** the provider is fully
  **inert** — 0 credentials, no filtering — so later app pushes never lock the
  machine and a stock logon is shown.

### Technician unlock

A discreet **"Technician unlock"** command link on the tile reveals a PIN field
when clicked (reliable LogonUI input routing — a hidden global hotkey on the
secure logon desktop proved unreliable across Windows builds). The entered PIN is
handed to the agent via the `pin-request`/`pin-response` files; the agent
validates it against AutoDeploy's existing rate-limited Access PIN and replies
`allow`/`deny`. The DLL holds **no crypto** — validation is delegated to the
agent. If no Access PIN is configured, the agent grants any submission.

### Dismissing the screen

LogonUI evaluates the credential provider only at load, so a displayed lock
screen can't be dismissed in place by clearing the marker. Both exit paths
therefore **reboot** to a clean logon (driven by the agent, which has shutdown
rights):

- **Normal completion:** the agent clears the marker and reboots once it closes
  the initial deployment — the machine comes back to the standard logon for
  first use, untouched.
- **Technician unlock:** a valid PIN makes the agent clear the marker and reboot.

## Building

Built with **MinGW-w64** so CI compiles it on Linux next to the Go components —
no Windows runner required:

```sh
sudo apt-get install -y g++-mingw-w64-x86-64
make build           # -> bin/autodeploy-credprovider-windows-amd64.dll
```

The DLL statically links libgcc/libstdc++ (`-static …`), so it has **no MinGW
runtime dependency** on the target machine — it imports only system DLLs
(`kernel32`, `user32`, `gdi32`, `advapi32`, `ole32`, `msvcrt`).

It can also be built with MSVC/ATL if preferred; the code uses only plain Win32 +
COM (no ATL) so either toolchain works.

## Registration

`DllRegisterServer`/`DllUnregisterServer` register the COM class and the
credential-provider entry under
`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Authentication\Credential Providers`.
The boot client's `SetupComplete.cmd` runs `regsvr32 /s` on the injected DLL at
deploy time (only for images that opted into the lock).

CLSID: `{A1E7F3C2-9B4D-4E6A-8C12-7F5E0A2B6D34}`.

## Safety

The provider **fails open**: any doubt (no marker, unreadable state) shows the
normal logon rather than locking everyone out. The technician PIN is the escape
hatch if the agent stalls. Registering a credential provider is security
sensitive — the released DLL should be code-signed for production (its SHA-256 is
already verified end-to-end through the distribution chain).
