#include "lockstate.h"

namespace lockstate {

static std::wstring toW(const std::string& s) {
    if (s.empty()) return std::wstring();
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), nullptr, 0);
    if (n <= 0) return std::wstring();
    std::wstring w((size_t)n, L'\0');
    MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), &w[0], n);
    return w;
}

static std::string toU8(const std::wstring& w) {
    if (w.empty()) return std::string();
    int n = WideCharToMultiByte(CP_UTF8, 0, w.data(), (int)w.size(), nullptr, 0, nullptr, nullptr);
    if (n <= 0) return std::string();
    std::string s((size_t)n, '\0');
    WideCharToMultiByte(CP_UTF8, 0, w.data(), (int)w.size(), &s[0], n, nullptr, nullptr);
    return s;
}

// ReadFileUTF8 reads a whole file as bytes, sharing write/delete so the agent's
// atomic rename is never blocked. Returns "" on any error (file absent, etc.).
static std::string ReadFileUTF8(const std::wstring& path) {
    HANDLE h = CreateFileW(path.c_str(), GENERIC_READ,
                           FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                           nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (h == INVALID_HANDLE_VALUE) return std::string();
    std::string out;
    char buf[4096];
    DWORD n = 0;
    while (ReadFile(h, buf, sizeof(buf), &n, nullptr) && n > 0) out.append(buf, n);
    CloseHandle(h);
    return out;
}

// jsonString extracts a flat JSON string field (handles the few escapes our
// agent emits). Good enough for the agent's compact, machine-generated JSON.
static bool jsonString(const std::string& j, const char* key, std::string& out) {
    std::string pat = std::string("\"") + key + "\"";
    size_t k = j.find(pat);
    if (k == std::string::npos) return false;
    size_t c = j.find(':', k + pat.size());
    if (c == std::string::npos) return false;
    size_t q = j.find('"', c);
    if (q == std::string::npos) return false;
    std::string v;
    for (size_t i = q + 1; i < j.size(); ++i) {
        char ch = j[i];
        if (ch == '\\' && i + 1 < j.size()) {
            char nx = j[++i];
            switch (nx) {
                case 'n': v += '\n'; break;
                case 't': v += '\t'; break;
                case '"': v += '"'; break;
                case '\\': v += '\\'; break;
                case '/': v += '/'; break;
                default: v += nx; break;
            }
        } else if (ch == '"') {
            break;
        } else {
            v += ch;
        }
    }
    out = v;
    return true;
}

static bool jsonInt(const std::string& j, const char* key, int& out) {
    std::string pat = std::string("\"") + key + "\"";
    size_t k = j.find(pat);
    if (k == std::string::npos) return false;
    size_t c = j.find(':', k + pat.size());
    if (c == std::string::npos) return false;
    size_t i = c + 1;
    while (i < j.size() && (j[i] == ' ' || j[i] == '\t')) ++i;
    bool neg = false;
    if (i < j.size() && j[i] == '-') { neg = true; ++i; }
    long val = 0;
    bool any = false;
    for (; i < j.size() && j[i] >= '0' && j[i] <= '9'; ++i) {
        val = val * 10 + (j[i] - '0');
        any = true;
    }
    if (!any) return false;
    out = (int)(neg ? -val : val);
    return true;
}

static COLORREF parseColor(const std::string& s, COLORREF def) {
    size_t i = 0;
    if (i < s.size() && s[i] == '#') ++i;
    if (s.size() - i < 6) return def;
    auto hx = [](char ch) -> int {
        if (ch >= '0' && ch <= '9') return ch - '0';
        if (ch >= 'a' && ch <= 'f') return ch - 'a' + 10;
        if (ch >= 'A' && ch <= 'F') return ch - 'A' + 10;
        return -1;
    };
    int d[6];
    for (int k = 0; k < 6; ++k) {
        d[k] = hx(s[i + k]);
        if (d[k] < 0) return def;
    }
    return RGB(d[0] * 16 + d[1], d[2] * 16 + d[3], d[4] * 16 + d[5]);
}

std::wstring Dir() {
    wchar_t buf[MAX_PATH];
    DWORD n = GetEnvironmentVariableW(L"ProgramData", buf, MAX_PATH);
    std::wstring base = (n > 0 && n < MAX_PATH) ? std::wstring(buf) : std::wstring(L"C:\\ProgramData");
    return base + L"\\AutoDeploy\\lock";
}

std::wstring Path(const wchar_t* name) {
    return Dir() + L"\\" + name;
}

bool MarkerPresent() {
    DWORD a = GetFileAttributesW(Path(L"active").c_str());
    return a != INVALID_FILE_ATTRIBUTES && !(a & FILE_ATTRIBUTE_DIRECTORY);
}

void ClearMarker() {
    DeleteFileW(Path(L"active").c_str());
}

Status ReadStatus() {
    Status st;
    std::string j = ReadFileUTF8(Path(L"status.json"));
    if (j.empty()) return st;
    std::string s;
    if (jsonString(j, "activity", s) && !s.empty()) st.activity = toW(s);
    if (jsonString(j, "phase", s)) st.phase = toW(s);
    jsonInt(j, "percent", st.percent);
    jsonInt(j, "done", st.done);
    jsonInt(j, "total", st.total);
    return st;
}

Branding ReadBranding() {
    Branding b;
    std::string j = ReadFileUTF8(Path(L"branding.json"));
    if (j.empty()) return b;
    std::string s;
    if (jsonString(j, "product_name", s) && !s.empty()) b.product = toW(s);
    if (jsonString(j, "organisation_name", s)) b.organisation = toW(s);
    if (jsonString(j, "support_url", s)) b.supportURL = toW(s);
    if (jsonString(j, "support_phone", s)) b.supportPhone = toW(s);
    if (jsonString(j, "primary_color", s)) b.primary = parseColor(s, b.primary);
    return b;
}

int RequestUnlock(const std::wstring& pin, DWORD timeoutMs) {
    std::wstring reqTmp = Path(L"pin-request.tmp");
    std::wstring req = Path(L"pin-request");
    std::wstring resp = Path(L"pin-response");
    DeleteFileW(resp.c_str()); // drop any stale verdict

    std::string u8 = toU8(pin);
    HANDLE h = CreateFileW(reqTmp.c_str(), GENERIC_WRITE, 0, nullptr,
                           CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (h == INVALID_HANDLE_VALUE) return -1;
    DWORD wr = 0;
    if (!u8.empty()) WriteFile(h, u8.data(), (DWORD)u8.size(), &wr, nullptr);
    CloseHandle(h);
    if (!MoveFileExW(reqTmp.c_str(), req.c_str(), MOVEFILE_REPLACE_EXISTING)) {
        DeleteFileW(reqTmp.c_str());
        return -1;
    }

    DWORD waited = 0;
    while (waited < timeoutMs) {
        Sleep(100);
        waited += 100;
        std::string r = ReadFileUTF8(resp);
        if (!r.empty()) {
            DeleteFileW(resp.c_str());
            return (r.find("allow") != std::string::npos) ? 1 : 0;
        }
    }
    return -1; // agent not responding
}

} // namespace lockstate
