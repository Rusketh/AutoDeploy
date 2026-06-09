// fullscreen.h -- the branded, full-screen setup screen the credential provider
// paints on EVERY connected monitor while the lock is armed. It runs on its own
// UI thread (it lives inside LogonUI on the Winlogon desktop, so it can create
// top-level windows there) and repaints from lockstate on a timer. Best-effort:
// if window creation fails the credential tile still shows the same progress, so
// the lock never depends on this succeeding.
#pragma once

#include <windows.h>

namespace fullscreen {

// Start brings up one branded window per monitor (idempotent). hinst is the
// DLL instance used to register the window class.
void Start(HINSTANCE hinst);
// Stop tears the windows down (idempotent).
void Stop();

} // namespace fullscreen
