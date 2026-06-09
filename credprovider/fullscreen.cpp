#include "fullscreen.h"
#include "lockstate.h"

#include <vector>

namespace fullscreen {

static const wchar_t* kClass = L"AutoDeploySetupLockWindow";
static const UINT WM_APP_STOP = WM_APP + 7;

static HINSTANCE g_hinst = nullptr;
static HANDLE g_thread = nullptr;
static DWORD g_threadId = 0;
static std::vector<HWND> g_windows;
static bool g_classRegistered = false;

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
    // Leave the PRIMARY monitor to LogonUI: the branded credential tile (with
    // its progress and the technician-unlock link) lives there and must stay
    // clickable. A full-screen window over it would block all interaction.
    // Secondary monitors get the full branded screen.
    if (mi.dwFlags & MONITORINFOF_PRIMARY) return TRUE;
    RECT r = mi.rcMonitor;
    HWND h = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, kClass, L"",
                             WS_POPUP, r.left, r.top, r.right - r.left, r.bottom - r.top,
                             nullptr, nullptr, g_hinst, nullptr);
    if (h) g_windows.push_back(h);
    return TRUE;
}

static DWORD WINAPI threadProc(LPVOID) {
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
    // Branded windows on every NON-primary monitor. On a single-monitor system
    // there are none -- the branded credential tile on the primary is the whole
    // UI, which is correct (a window there would block the unlock link).
    EnumDisplayMonitors(nullptr, nullptr, monitorEnum, 0);
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
