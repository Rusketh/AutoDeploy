// lockstate.h -- the AutoDeploy setup-lock screen's view of the on-disk state
// the agent maintains. The credential provider and the agent run in different
// sessions, so they coordinate through a fixed directory under %ProgramData%:
//
//   active        -- present => the lock is armed (cover the logon screen)
//   status.json   -- {phase, activity, done, total, percent}  (live progress)
//   branding.json -- {product_name, organisation_name, primary_color, ...}
//   pin-request   -- written by us, the entered technician PIN
//   pin-response  -- written by the agent, "allow" or "deny"
//
// This module is pure Win32 (no COM) so it can be unit-reasoned in isolation.
#pragma once

#include <windows.h>
#include <string>

namespace lockstate {

// Dir is the fixed lock-state directory (%ProgramData%\AutoDeploy\lock).
std::wstring Dir();
// Path joins a filename onto Dir.
std::wstring Path(const wchar_t* name);

// MarkerPresent reports whether the "active" marker exists (lock armed).
bool MarkerPresent();
// ClearMarker removes the "active" marker so the next logon is normal.
void ClearMarker();

struct Status {
    std::wstring activity = L"Preparing your computer…";
    std::wstring phase;
    int percent = -1; // -1 = indeterminate
    int done = 0;
    int total = 0;
};
// ReadStatus returns the current status, or sensible defaults when the file is
// missing or unreadable.
Status ReadStatus();

struct Branding {
    std::wstring product = L"AutoDeploy";
    std::wstring organisation;
    std::wstring supportURL;
    std::wstring supportPhone;
    COLORREF primary = RGB(0x0b, 0x65, 0xc2);
};
// ReadBranding returns operator branding, or defaults when absent.
Branding ReadBranding();

// RequestUnlock submits a technician PIN to the agent over the file protocol
// and waits up to timeoutMs for a verdict. Returns 1 (allow), 0 (deny), or
// -1 (timeout / agent not responding).
int RequestUnlock(const std::wstring& pin, DWORD timeoutMs);

} // namespace lockstate
