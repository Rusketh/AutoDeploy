#include "fullscreen.h"
#include "lockstate.h"

#include <vector>
#include <string>
#include <cstring>
#include <objbase.h>
#include <gdiplus.h>

namespace fullscreen {

static const wchar_t* kClass = L"AutoDeploySetupLockWindow";
static const wchar_t* kUnlockText = L"Technician unlock";
static const UINT WM_APP_STOP = WM_APP + 7;

static HINSTANCE g_hinst = nullptr;
static HANDLE g_thread = nullptr;
static DWORD g_threadId = 0;
static std::vector<HWND> g_windows;
static bool g_classRegistered = false;
static volatile LONG g_unlockRequested = 0;

// GDI+ token for the lock-screen thread, plus a one-entry cache of the decoded
// logo keyed on its data URL so the 500ms repaint doesn't re-decode every tick.
static ULONG_PTR g_gdiplusToken = 0;
static std::string g_logoKey;
static Gdiplus::Bitmap* g_logoBmp = nullptr;

// base64Decode decodes standard base64, skipping whitespace and stopping at
// padding. Returns the raw bytes (empty on no input).
static std::string base64Decode(const std::string& in) {
    auto val = [](unsigned char c) -> int {
        if (c >= 'A' && c <= 'Z') return c - 'A';
        if (c >= 'a' && c <= 'z') return c - 'a' + 26;
        if (c >= '0' && c <= '9') return c - '0' + 52;
        if (c == '+') return 62;
        if (c == '/') return 63;
        return -1;
    };
    std::string out;
    int buf = 0, bits = 0;
    for (unsigned char c : in) {
        if (c == '=') break;
        int v = val(c);
        if (v < 0) continue; // skip newlines / stray whitespace
        buf = (buf << 6) | v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            out.push_back((char)((buf >> bits) & 0xFF));
        }
    }
    return out;
}

// decodeLogoBitmap turns a "data:image/...;base64,…" URL into an owned GDI+
// bitmap (caller deletes), or nullptr for anything it can't decode. The decoded
// bytes are streamed to GDI+, then cloned into a standalone ARGB bitmap so the
// backing stream can be released immediately. Best-effort: every failure path
// returns nullptr and the screen just shows no logo.
static Gdiplus::Bitmap* decodeLogoBitmap(const std::string& dataURL) {
    if (g_gdiplusToken == 0) return nullptr;
    if (dataURL.compare(0, 11, "data:image/") != 0) return nullptr;
    size_t m = dataURL.find("base64,");
    if (m == std::string::npos) return nullptr;
    std::string bytes = base64Decode(dataURL.substr(m + 7));
    if (bytes.size() < 8) return nullptr;

    HGLOBAL hMem = GlobalAlloc(GMEM_MOVEABLE, bytes.size());
    if (!hMem) return nullptr;
    void* p = GlobalLock(hMem);
    if (!p) { GlobalFree(hMem); return nullptr; }
    memcpy(p, bytes.data(), bytes.size());
    GlobalUnlock(hMem);

    IStream* stream = nullptr;
    if (CreateStreamOnHGlobal(hMem, TRUE, &stream) != S_OK || !stream) {
        GlobalFree(hMem);
        return nullptr;
    }
    Gdiplus::Bitmap* tmp = Gdiplus::Bitmap::FromStream(stream);
    Gdiplus::Bitmap* owned = nullptr;
    if (tmp && tmp->GetLastStatus() == Gdiplus::Ok &&
        tmp->GetWidth() > 0 && tmp->GetHeight() > 0) {
        owned = tmp->Clone(0, 0, (INT)tmp->GetWidth(), (INT)tmp->GetHeight(),
                           PixelFormat32bppARGB);
        if (owned && owned->GetLastStatus() != Gdiplus::Ok) {
            delete owned;
            owned = nullptr;
        }
    }
    if (tmp) delete tmp;
    stream->Release(); // frees hMem (CreateStreamOnHGlobal fDeleteOnRelease=TRUE)
    return owned;
}

bool ConsumeUnlockRequest() {
    return InterlockedCompareExchange(&g_unlockRequested, 0, 1) == 1;
}

// blend mixes two colours by t/255.
static COLORREF blend(COLORREF a, COLORREF b, int t) {
    int ar = GetRValue(a), ag = GetGValue(a), ab = GetBValue(a);
    int br = GetRValue(b), bg = GetGValue(b), bb = GetBValue(b);
    return RGB(ar + (br - ar) * t / 255, ag + (bg - ag) * t / 255, ab + (bb - ab) * t / 255);
}

static HFONT makeFont(int px, int weight) {
    return CreateFontW(-px, 0, 0, 0, weight, FALSE, FALSE, FALSE, DEFAULT_CHARSET,
                       OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS, CLEARTYPE_QUALITY,
                       VARIABLE_PITCH | FF_SWISS, L"Segoe UI");
}

static void drawCentered(HDC dc, const std::wstring& s, int cx, int y, HFONT f, COLORREF col) {
    if (s.empty()) return;
    HGDIOBJ old = SelectObject(dc, f);
    SetTextColor(dc, col);
    SetBkMode(dc, TRANSPARENT);
    SIZE sz{};
    GetTextExtentPoint32W(dc, s.c_str(), (int)s.size(), &sz);
    TextOutW(dc, cx - sz.cx / 2, y, s.c_str(), (int)s.size());
    SelectObject(dc, old);
}

// linkRect returns where the discreet "Technician unlock" link sits (bottom-right
// corner). paint() and the mouse handlers share this so the hit-test always
// matches what was drawn.
static RECT linkRect(HWND hwnd) {
    RECT rc;
    GetClientRect(hwnd, &rc);
    int w = rc.right - rc.left, h = rc.bottom - rc.top;
    int unit = (w < h ? w : h);
    int margin = unit / 30 + 8;
    SIZE sz{};
    HDC dc = GetDC(hwnd);
    HFONT f = makeFont(unit / 60 + 5, FW_NORMAL);
    HGDIOBJ old = SelectObject(dc, f);
    GetTextExtentPoint32W(dc, kUnlockText, (int)wcslen(kUnlockText), &sz);
    SelectObject(dc, old);
    DeleteObject(f);
    ReleaseDC(hwnd, dc);
    RECT r{w - margin - sz.cx, h - margin - sz.cy, w - margin, h - margin};
    return r;
}

static void paint(HWND hwnd) {
    RECT rc;
    GetClientRect(hwnd, &rc);
    int w = rc.right - rc.left, h = rc.bottom - rc.top;
    if (w <= 0 || h <= 0) return;

    PAINTSTRUCT ps;
    HDC sdc = BeginPaint(hwnd, &ps);

    // Double-buffer to avoid flicker on the 500ms repaint.
    HDC dc = CreateCompatibleDC(sdc);
    HBITMAP bmp = CreateCompatibleBitmap(sdc, w, h);
    HGDIOBJ oldBmp = SelectObject(dc, bmp);

    lockstate::Branding br = lockstate::ReadBranding();
    lockstate::Status st = lockstate::ReadStatus();

    const COLORREF bg = RGB(0x0a, 0x0f, 0x16);     // dark slate (matches the mockup)
    const COLORREF text = RGB(0xee, 0xf3, 0xf8);
    const COLORREF muted = RGB(0x8a, 0xa0, 0xb4);
    const COLORREF track = RGB(0x1d, 0x29, 0x37);

    // Background: dark, with a subtle wash of the brand colour near the top.
    HBRUSH bgBrush = CreateSolidBrush(bg);
    FillRect(dc, &rc, bgBrush);
    DeleteObject(bgBrush);
    for (int i = 0; i < h / 3; ++i) {
        int t = 40 - (40 * i) / (h / 3 + 1); // fade out
        if (t <= 0) break;
        HBRUSH line = CreateSolidBrush(blend(bg, br.primary, t));
        RECT lr{0, i, w, i + 1};
        FillRect(dc, &lr, line);
        DeleteObject(line);
    }

    int unit = (w < h ? w : h);
    HFONT fTitle = makeFont(unit / 18 + 8, FW_SEMIBOLD);
    HFONT fOrg = makeFont(unit / 36 + 6, FW_SEMIBOLD);
    HFONT fAct = makeFont(unit / 40 + 6, FW_NORMAL);
    HFONT fSmall = makeFont(unit / 60 + 5, FW_NORMAL);

    int cx = w / 2;
    int y = h / 3;

    // Operator logo, drawn centred just above the organisation line. Cached by
    // data URL so the repaint timer doesn't re-decode. Best-effort: an unset or
    // undecodable logo simply leaves the text-only layout untouched.
    if (br.logoDataURL != g_logoKey) {
        if (g_logoBmp) { delete g_logoBmp; g_logoBmp = nullptr; }
        g_logoBmp = br.logoDataURL.empty() ? nullptr : decodeLogoBitmap(br.logoDataURL);
        g_logoKey = br.logoDataURL;
    }
    if (g_logoBmp) {
        UINT iw = g_logoBmp->GetWidth(), ih = g_logoBmp->GetHeight();
        if (iw > 0 && ih > 0) {
            int lh = unit / 8;
            int lw = (int)((long long)iw * lh / ih);
            int maxW = w / 3;
            if (lw > maxW) {
                lw = maxW;
                lh = (int)((long long)ih * lw / iw);
            }
            int lx = cx - lw / 2;
            int ly = y - lh - unit / 24;
            if (ly < unit / 24) ly = unit / 24;
            Gdiplus::Graphics g(dc);
            g.SetInterpolationMode(Gdiplus::InterpolationModeHighQualityBicubic);
            g.DrawImage(g_logoBmp, lx, ly, lw, lh);
        }
    }

    std::wstring org = !br.organisation.empty() ? br.organisation : br.product;
    drawCentered(dc, org, cx, y, fOrg, muted);
    y += unit / 36 + 24;
    drawCentered(dc, L"Setting up your computer", cx, y, fTitle, text);
    y += unit / 18 + 28;
    drawCentered(dc, st.activity, cx, y, fAct, text);
    y += unit / 40 + 30;

    // Progress bar.
    int barW = w / 3;
    int barH = (unit / 90 < 8) ? 8 : unit / 90;
    int barX = cx - barW / 2;
    RECT tr{barX, y, barX + barW, y + barH};
    HBRUSH tb = CreateSolidBrush(track);
    FillRect(dc, &tr, tb);
    DeleteObject(tb);
    int pct = st.percent;
    if (pct < 0) {
        // Indeterminate: a moving segment based on the tick count.
        int seg = barW / 4;
        int pos = (int)((GetTickCount() / 12) % (DWORD)(barW + seg)) - seg;
        RECT fr{barX + (pos < 0 ? 0 : pos), y,
                barX + ((pos + seg > barW) ? barW : pos + seg), y + barH};
        HBRUSH fb = CreateSolidBrush(br.primary);
        FillRect(dc, &fr, fb);
        DeleteObject(fb);
    } else {
        if (pct > 100) pct = 100;
        RECT fr{barX, y, barX + barW * pct / 100, y + barH};
        HBRUSH fb = CreateSolidBrush(br.primary);
        FillRect(dc, &fr, fb);
        DeleteObject(fb);
    }
    y += barH + 16;
    if (st.percent >= 0) {
        wchar_t pctText[16];
        wsprintfW(pctText, L"%d%%", st.percent);
        drawCentered(dc, pctText, cx, y, fSmall, muted);
        y += unit / 60 + 18;
    }

    drawCentered(dc, L"Please keep this computer plugged in and powered on.", cx,
                 h - h / 8, fSmall, muted);

    // Discreet technician-unlock link, bottom-right. Clicking it hides the
    // branded windows and reveals the PIN field on the credential tile.
    RECT lr = linkRect(hwnd);
    HGDIOBJ oldFont = SelectObject(dc, fSmall);
    SetTextColor(dc, muted);
    SetBkMode(dc, TRANSPARENT);
    TextOutW(dc, lr.left, lr.top, kUnlockText, (int)wcslen(kUnlockText));
    SelectObject(dc, oldFont);

    BitBlt(sdc, 0, 0, w, h, dc, 0, 0, SRCCOPY);

    DeleteObject(fTitle);
    DeleteObject(fOrg);
    DeleteObject(fAct);
    DeleteObject(fSmall);
    SelectObject(dc, oldBmp);
    DeleteObject(bmp);
    DeleteDC(dc);
    EndPaint(hwnd, &ps);
}

static LRESULT CALLBACK WndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
        case WM_TIMER:
            InvalidateRect(hwnd, nullptr, FALSE);
            return 0;
        case WM_PAINT:
            paint(hwnd);
            return 0;
        case WM_ERASEBKGND:
            return 1; // fully painted in WM_PAINT; suppress flicker
        case WM_SETCURSOR: {
            POINT pt;
            if (GetCursorPos(&pt)) {
                ScreenToClient(hwnd, &pt);
                RECT lr = linkRect(hwnd);
                if (PtInRect(&lr, pt)) {
                    SetCursor(LoadCursorW(nullptr, (LPCWSTR)IDC_HAND));
                    return TRUE;
                }
            }
            return DefWindowProcW(hwnd, msg, wp, lp);
        }
        case WM_LBUTTONUP: {
            POINT pt{(short)LOWORD(lp), (short)HIWORD(lp)};
            RECT lr = linkRect(hwnd);
            if (PtInRect(&lr, pt)) {
                // Window-local mouse input is reliable on the secure desktop
                // (unlike the global hotkey this design originally used). Hide
                // every branded window so the credential tile underneath is
                // fully interactive, then let the credential's update thread
                // pick up the request and reveal the PIN field.
                InterlockedExchange(&g_unlockRequested, 1);
                for (HWND h : g_windows) ShowWindow(h, SW_HIDE);
            }
            return 0;
        }
        case WM_CLOSE:
            return 0; // refuse to be closed by stray input
        default:
            return DefWindowProcW(hwnd, msg, wp, lp);
    }
}

static BOOL CALLBACK monitorEnum(HMONITOR hMon, HDC, LPRECT, LPARAM) {
    MONITORINFO mi;
    mi.cbSize = sizeof(mi);
    if (!GetMonitorInfoW(hMon, &mi)) return TRUE;
    RECT r = mi.rcMonitor;
    HWND h = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, kClass, L"",
                             WS_POPUP, r.left, r.top, r.right - r.left, r.bottom - r.top,
                             nullptr, nullptr, g_hinst, nullptr);
    if (h) g_windows.push_back(h);
    return TRUE;
}

static DWORD WINAPI threadProc(LPVOID) {
    // GDI+ for the lifetime of the lock screen, so paint() can render an
    // operator logo. Failure is non-fatal: decodeLogoBitmap checks the token.
    Gdiplus::GdiplusStartupInput gpsi;
    Gdiplus::GdiplusStartup(&g_gdiplusToken, &gpsi, nullptr);
    if (!g_classRegistered) {
        WNDCLASSEXW wc;
        ZeroMemory(&wc, sizeof(wc));
        wc.cbSize = sizeof(wc);
        wc.lpfnWndProc = WndProc;
        wc.hInstance = g_hinst;
        wc.hCursor = LoadCursor(nullptr, IDC_ARROW);
        wc.lpszClassName = kClass;
        RegisterClassExW(&wc);
        g_classRegistered = true;
    }
    // Branded windows on EVERY monitor, primary included: the full-screen
    // screen is the product's face during rollout. The credential tile it
    // covers stays reachable because the discreet unlock link on the branded
    // window hides all of these on click.
    EnumDisplayMonitors(nullptr, nullptr, monitorEnum, 0);
    if (g_windows.empty()) {
        // Fallback: a single window spanning the whole virtual desktop.
        int vx = GetSystemMetrics(SM_XVIRTUALSCREEN);
        int vy = GetSystemMetrics(SM_YVIRTUALSCREEN);
        int vw = GetSystemMetrics(SM_CXVIRTUALSCREEN);
        int vh = GetSystemMetrics(SM_CYVIRTUALSCREEN);
        HWND h = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, kClass, L"",
                                 WS_POPUP, vx, vy, vw, vh, nullptr, nullptr, g_hinst, nullptr);
        if (h) g_windows.push_back(h);
    }
    for (HWND h : g_windows) {
        ShowWindow(h, SW_SHOW);
        SetTimer(h, 1, 500, nullptr);
    }

    MSG msg;
    while (GetMessageW(&msg, nullptr, 0, 0) > 0) {
        if (msg.message == WM_APP_STOP) break;
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    for (HWND h : g_windows) DestroyWindow(h);
    g_windows.clear();
    // Release the cached logo (a GDI+ object) before shutting GDI+ down.
    if (g_logoBmp) { delete g_logoBmp; g_logoBmp = nullptr; }
    g_logoKey.clear();
    if (g_gdiplusToken) {
        Gdiplus::GdiplusShutdown(g_gdiplusToken);
        g_gdiplusToken = 0;
    }
    return 0;
}

void Start(HINSTANCE hinst) {
    if (g_thread) return; // already running
    g_hinst = hinst;
    g_thread = CreateThread(nullptr, 0, threadProc, nullptr, 0, &g_threadId);
}

void Stop() {
    if (!g_thread) return;
    PostThreadMessageW(g_threadId, WM_APP_STOP, 0, 0);
    WaitForSingleObject(g_thread, 3000);
    CloseHandle(g_thread);
    g_thread = nullptr;
    g_threadId = 0;
}

} // namespace fullscreen
