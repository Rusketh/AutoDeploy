// fullscreen.h -- the branded, full-screen setup screen the credential provider
// paints on EVERY connected monitor while the lock is armed. It runs on its own
// UI thread (it lives inside LogonUI on the Winlogon desktop, so it can create
// top-level windows there) and repaints from lockstate on a timer. Best-effort:
// if window creation fails the credential tile still shows the same progress
// (and its own unlock link), so the lock never depends on this succeeding.
//
// Each branded window carries a discreet "Technician unlock" link in its
// bottom-right corner. Clicking it hides every branded window -- exposing the
// interactive credential tile -- and records an unlock request that the
// credential's update thread consumes via ConsumeUnlockRequest() to reveal the
// PIN field. Window-local mouse input is reliable on the secure desktop, unlike
// the hidden global hotkey this design originally used.
#pragma once

#include <windows.h>

namespace fullscreen {

// Start brings up one branded window per monitor (idempotent). hinst is the
// DLL instance used to register the window class.
void Start(HINSTANCE hinst);
// Stop tears the windows down (idempotent).
void Stop();
// ConsumeUnlockRequest reports -- and clears -- a pending technician-unlock
// request made by clicking the link on a branded window.
bool ConsumeUnlockRequest();

} // namespace fullscreen
