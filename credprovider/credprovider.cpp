// credprovider.cpp -- the AutoDeploy "setup lock" Windows Credential Provider.
//
// While the on-disk lock marker is present (armed at deploy time by
// SetupComplete.cmd, maintained by the agent during the initial software
// rollout), this provider:
//   * filters out every OTHER credential provider, so a regular user cannot
//     sign in (ICredentialProviderFilter);
//   * shows a branded tile with the live activity + progress the agent writes
//     to status.json, refreshed on a background thread; and
//   * paints a branded full-screen screen on every monitor (fullscreen.cpp).
// A discreet "Technician unlock" link on the branded screen hides it and
// reveals a PIN field on the tile; the tile carries the same command link as a
// fallback for when the branded windows cannot be created. The entered PIN is
// validated by the agent (file protocol in lockstate.cpp) and, on success, the
// marker is cleared so normal sign-in returns.
//
// When the marker is absent the provider is completely inert (0 credentials, no
// filtering), so a machine past its initial rollout behaves exactly as stock.

#include <initguid.h>
#include <windows.h>
#include <credentialprovider.h>
#include <new>
#include <string>

#include "lockstate.h"
#include "fullscreen.h"

// {A1E7F3C2-9B4D-4E6A-8C12-7F5E0A2B6D34}
DEFINE_GUID(CLSID_AutoDeployProvider,
            0xa1e7f3c2, 0x9b4d, 0x4e6a, 0x8c, 0x12, 0x7f, 0x5e, 0x0a, 0x2b, 0x6d, 0x34);
static const wchar_t* kClsidStr = L"{A1E7F3C2-9B4D-4E6A-8C12-7F5E0A2B6D34}";
static const wchar_t* kFriendly = L"AutoDeploy Setup Lock";

static HINSTANCE g_hinst = nullptr;
static LONG g_cDllRef = 0;

// Fields shown on the tile.
enum FIELD {
    FID_TITLE = 0,    // large text: "Setting up your computer"
    FID_ACTIVITY,     // small text: the agent's current activity
    FID_PROGRESS,     // small text: textual progress bar
    FID_UNLOCK_LINK,  // command link: discreet "Technician unlock"
    FID_PIN,          // password: technician PIN (revealed by the link)
    FID_SUBMIT,       // submit button for the PIN (revealed by the link)
    FID_COUNT
};

static CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR g_fields[FID_COUNT] = {
    {FID_TITLE, CPFT_LARGE_TEXT, (PWSTR)L"Setting up your computer"},
    {FID_ACTIVITY, CPFT_SMALL_TEXT, (PWSTR)L"Activity"},
    {FID_PROGRESS, CPFT_SMALL_TEXT, (PWSTR)L"Progress"},
    {FID_UNLOCK_LINK, CPFT_COMMAND_LINK, (PWSTR)L"Technician unlock"},
    {FID_PIN, CPFT_PASSWORD_TEXT, (PWSTR)L"Technician PIN"},
    {FID_SUBMIT, CPFT_SUBMIT_BUTTON, (PWSTR)L"Unlock"},
};

// dupW allocates a CoTaskMem copy of s (the caller frees with CoTaskMemFree),
// the ownership contract every PWSTR* out-param in this API expects.
static HRESULT dupW(const wchar_t* s, PWSTR* out) {
    if (!s) s = L"";
    size_t cb = (wcslen(s) + 1) * sizeof(wchar_t);
    PWSTR p = (PWSTR)CoTaskMemAlloc(cb);
    if (!p) return E_OUTOFMEMORY;
    memcpy(p, s, cb);
    *out = p;
    return S_OK;
}

static std::wstring progressText(const lockstate::Status& st) {
    if (st.percent < 0) return L"Working…";
    int filled = st.percent / 10;
    std::wstring bar = L"[";
    for (int i = 0; i < 10; ++i) bar += (i < filled ? L"█" : L"░");
    wchar_t pct[16];
    wsprintfW(pct, L"]  %d%%", st.percent);
    return bar + pct;
}

// ---------------------------------------------------------------------------
// Credential
// ---------------------------------------------------------------------------
class CAutoDeployCredential : public ICredentialProviderCredential {
  public:
    CAutoDeployCredential() : m_cRef(1) {}
    virtual ~CAutoDeployCredential() {}

    // IUnknown
    IFACEMETHODIMP QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        if (IsEqualIID(riid, IID_IUnknown) ||
            IsEqualIID(riid, __uuidof(ICredentialProviderCredential))) {
            *ppv = static_cast<ICredentialProviderCredential*>(this);
            AddRef();
            return S_OK;
        }
        *ppv = nullptr;
        return E_NOINTERFACE;
    }
    IFACEMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&m_cRef); }
    IFACEMETHODIMP_(ULONG) Release() override {
        LONG n = InterlockedDecrement(&m_cRef);
        if (n == 0) delete this;
        return n;
    }

    // ICredentialProviderCredential
    IFACEMETHODIMP Advise(ICredentialProviderCredentialEvents* e) override {
        if (m_pEvents) m_pEvents->Release();
        m_pEvents = e;
        if (m_pEvents) m_pEvents->AddRef();
        startUpdates();
        return S_OK;
    }
    IFACEMETHODIMP UnAdvise() override {
        stopUpdates();
        if (m_pEvents) {
            m_pEvents->Release();
            m_pEvents = nullptr;
        }
        return S_OK;
    }
    IFACEMETHODIMP SetSelected(BOOL* pbAutoLogon) override {
        if (pbAutoLogon) *pbAutoLogon = FALSE; // never auto-submit
        return S_OK;
    }
    IFACEMETHODIMP SetDeselected() override {
        m_pin.clear();
        return S_OK;
    }
    IFACEMETHODIMP GetFieldState(DWORD f, CREDENTIAL_PROVIDER_FIELD_STATE* pcpfs,
                                 CREDENTIAL_PROVIDER_FIELD_INTERACTIVE_STATE* pcpfis) override {
        CREDENTIAL_PROVIDER_FIELD_STATE s = CPFS_HIDDEN;
        CREDENTIAL_PROVIDER_FIELD_INTERACTIVE_STATE is = CPFIS_NONE;
        switch (f) {
            case FID_TITLE:
            case FID_ACTIVITY:
            case FID_PROGRESS:
                s = CPFS_DISPLAY_IN_BOTH;
                break;
            case FID_UNLOCK_LINK:
                s = m_revealed ? CPFS_HIDDEN : CPFS_DISPLAY_IN_BOTH;
                break;
            case FID_PIN:
                s = m_revealed ? CPFS_DISPLAY_IN_SELECTED_TILE : CPFS_HIDDEN;
                is = m_revealed ? CPFIS_FOCUSED : CPFIS_NONE;
                break;
            case FID_SUBMIT:
                s = m_revealed ? CPFS_DISPLAY_IN_SELECTED_TILE : CPFS_HIDDEN;
                break;
            default:
                return E_INVALIDARG;
        }
        if (pcpfs) *pcpfs = s;
        if (pcpfis) *pcpfis = is;
        return S_OK;
    }
    IFACEMETHODIMP GetStringValue(DWORD f, PWSTR* ppwsz) override {
        if (!ppwsz) return E_POINTER;
        switch (f) {
            case FID_TITLE:
                return dupW(L"Setting up your computer", ppwsz);
            case FID_ACTIVITY:
                return dupW(lockstate::ReadStatus().activity.c_str(), ppwsz);
            case FID_PROGRESS:
                return dupW(progressText(lockstate::ReadStatus()).c_str(), ppwsz);
            case FID_UNLOCK_LINK:
                return dupW(L"Technician unlock", ppwsz);
            case FID_PIN:
                return dupW(L"", ppwsz);
            default:
                return dupW(L"", ppwsz);
        }
    }
    IFACEMETHODIMP GetBitmapValue(DWORD, HBITMAP*) override { return E_NOTIMPL; }
    IFACEMETHODIMP GetCheckboxValue(DWORD, BOOL*, PWSTR*) override { return E_NOTIMPL; }
    IFACEMETHODIMP GetSubmitButtonValue(DWORD f, DWORD* pdwAdjacentTo) override {
        if (f != FID_SUBMIT || !pdwAdjacentTo) return E_INVALIDARG;
        *pdwAdjacentTo = FID_PIN;
        return S_OK;
    }
    IFACEMETHODIMP GetComboBoxValueCount(DWORD, DWORD*, DWORD*) override { return E_NOTIMPL; }
    IFACEMETHODIMP GetComboBoxValueAt(DWORD, DWORD, PWSTR*) override { return E_NOTIMPL; }
    IFACEMETHODIMP SetStringValue(DWORD f, PCWSTR pwz) override {
        if (f == FID_PIN) {
            m_pin = pwz ? pwz : L"";
            return S_OK;
        }
        return E_INVALIDARG;
    }
    IFACEMETHODIMP SetCheckboxValue(DWORD, BOOL) override { return E_NOTIMPL; }
    IFACEMETHODIMP SetComboBoxSelectedValue(DWORD, DWORD) override { return E_NOTIMPL; }
    IFACEMETHODIMP CommandLinkClicked(DWORD f) override {
        if (f != FID_UNLOCK_LINK) return E_INVALIDARG;
        reveal();
        return S_OK;
    }
    IFACEMETHODIMP GetSerialization(CREDENTIAL_PROVIDER_GET_SERIALIZATION_RESPONSE* pcpgsr,
                                    CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION* pcpcs,
                                    PWSTR* ppwszOptionalStatusText,
                                    CREDENTIAL_PROVIDER_STATUS_ICON* pcpsiOptionalStatusIcon) override {
        if (pcpgsr) *pcpgsr = CPGSR_NO_CREDENTIAL_NOT_FINISHED;
        if (pcpcs) ZeroMemory(pcpcs, sizeof(*pcpcs));
        if (ppwszOptionalStatusText) *ppwszOptionalStatusText = nullptr;
        if (pcpsiOptionalStatusIcon) *pcpsiOptionalStatusIcon = CPSI_NONE;

        if (!m_revealed) return S_OK; // tile is progress-only until unlock is requested

        int verdict = lockstate::RequestUnlock(m_pin, 4000);
        m_pin.clear();
        if (verdict == 1) {
            // Technician authorised: drop the lock so normal sign-in returns.
            lockstate::ClearMarker();
            fullscreen::Stop();
            if (pcpgsr) *pcpgsr = CPGSR_NO_CREDENTIAL_FINISHED;
            if (ppwszOptionalStatusText)
                dupW(L"Unlocked. Choose another sign-in option.", ppwszOptionalStatusText);
            if (pcpsiOptionalStatusIcon) *pcpsiOptionalStatusIcon = CPSI_SUCCESS;
            return S_OK;
        }
        if (ppwszOptionalStatusText)
            dupW(verdict == 0 ? L"Incorrect PIN." : L"Setup agent is not responding.",
                 ppwszOptionalStatusText);
        if (pcpsiOptionalStatusIcon)
            *pcpsiOptionalStatusIcon = (verdict == 0) ? CPSI_ERROR : CPSI_WARNING;
        return S_OK;
    }
    IFACEMETHODIMP ReportResult(NTSTATUS, NTSTATUS, PWSTR* ppwszOptionalStatusText,
                                CREDENTIAL_PROVIDER_STATUS_ICON* pcpsiOptionalStatusIcon) override {
        if (ppwszOptionalStatusText) *ppwszOptionalStatusText = nullptr;
        if (pcpsiOptionalStatusIcon) *pcpsiOptionalStatusIcon = CPSI_NONE;
        return S_OK;
    }

  private:
    // reveal swaps the unlock link for the focused PIN field + submit button.
    // Called from CommandLinkClicked (tile link) and from the update thread
    // when the link on a branded full-screen window was clicked.
    void reveal() {
        if (m_revealed) return;
        m_revealed = true;
        if (m_pEvents) {
            m_pEvents->SetFieldState(this, FID_UNLOCK_LINK, CPFS_HIDDEN);
            m_pEvents->SetFieldState(this, FID_PIN, CPFS_DISPLAY_IN_SELECTED_TILE);
            m_pEvents->SetFieldInteractiveState(this, FID_PIN, CPFIS_FOCUSED);
            m_pEvents->SetFieldState(this, FID_SUBMIT, CPFS_DISPLAY_IN_SELECTED_TILE);
        }
    }

    // updateThread keeps the tile's activity + progress text live (LogonUI
    // caches field values until SetFieldString pushes a change) and watches for
    // an unlock request from the branded full-screen windows.
    static DWORD WINAPI updateThread(LPVOID param) {
        CAutoDeployCredential* self = (CAutoDeployCredential*)param;
        while (InterlockedCompareExchange(&self->m_stop, 0, 0) == 0) {
            if (self->m_pEvents) {
                lockstate::Status st = lockstate::ReadStatus();
                self->m_pEvents->SetFieldString(self, FID_ACTIVITY, st.activity.c_str());
                std::wstring pt = progressText(st);
                self->m_pEvents->SetFieldString(self, FID_PROGRESS, pt.c_str());
                if (fullscreen::ConsumeUnlockRequest()) self->reveal();
            }
            Sleep(300);
        }
        return 0;
    }
    void startUpdates() {
        InterlockedExchange(&m_stop, 0);
        if (!m_thread) m_thread = CreateThread(nullptr, 0, updateThread, this, 0, nullptr);
    }
    void stopUpdates() {
        InterlockedExchange(&m_stop, 1);
        if (m_thread) {
            WaitForSingleObject(m_thread, 2000);
            CloseHandle(m_thread);
            m_thread = nullptr;
        }
    }

    LONG m_cRef;
    ICredentialProviderCredentialEvents* m_pEvents = nullptr;
    std::wstring m_pin;
    bool m_revealed = false;
    HANDLE m_thread = nullptr;
    volatile LONG m_stop = 0;
};

// ---------------------------------------------------------------------------
// Provider (+ filter)
// ---------------------------------------------------------------------------
class CAutoDeployProvider : public ICredentialProvider, public ICredentialProviderFilter {
  public:
    CAutoDeployProvider() : m_cRef(1) {}
    virtual ~CAutoDeployProvider() {
        if (m_pCred) m_pCred->Release();
        if (m_pEvents) m_pEvents->Release();
    }

    // IUnknown
    IFACEMETHODIMP QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, __uuidof(ICredentialProvider))) {
            *ppv = static_cast<ICredentialProvider*>(this);
            AddRef();
            return S_OK;
        }
        if (IsEqualIID(riid, __uuidof(ICredentialProviderFilter))) {
            *ppv = static_cast<ICredentialProviderFilter*>(this);
            AddRef();
            return S_OK;
        }
        *ppv = nullptr;
        return E_NOINTERFACE;
    }
    IFACEMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&m_cRef); }
    IFACEMETHODIMP_(ULONG) Release() override {
        LONG n = InterlockedDecrement(&m_cRef);
        if (n == 0) delete this;
        return n;
    }

    // ICredentialProvider
    IFACEMETHODIMP SetUsageScenario(CREDENTIAL_PROVIDER_USAGE_SCENARIO cpus, DWORD) override {
        m_cpus = cpus;
        if (cpus != CPUS_LOGON && cpus != CPUS_UNLOCK_WORKSTATION) return E_NOTIMPL;
        m_locked = lockstate::MarkerPresent();
        if (m_locked) {
            if (!m_pCred) m_pCred = new (std::nothrow) CAutoDeployCredential();
            fullscreen::Start(g_hinst);
        }
        return S_OK;
    }
    IFACEMETHODIMP SetSerialization(const CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION*) override {
        return E_NOTIMPL;
    }
    IFACEMETHODIMP Advise(ICredentialProviderEvents* e, UINT_PTR ctx) override {
        if (m_pEvents) m_pEvents->Release();
        m_pEvents = e;
        if (m_pEvents) m_pEvents->AddRef();
        m_adviseCtx = ctx;
        return S_OK;
    }
    IFACEMETHODIMP UnAdvise() override {
        if (m_pEvents) {
            m_pEvents->Release();
            m_pEvents = nullptr;
        }
        return S_OK;
    }
    IFACEMETHODIMP GetFieldDescriptorCount(DWORD* pdwCount) override {
        if (!pdwCount) return E_POINTER;
        *pdwCount = FID_COUNT;
        return S_OK;
    }
    IFACEMETHODIMP GetFieldDescriptorAt(DWORD dwIndex,
                                        CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR** ppcpfd) override {
        if (dwIndex >= FID_COUNT || !ppcpfd) return E_INVALIDARG;
        CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR* p =
            (CREDENTIAL_PROVIDER_FIELD_DESCRIPTOR*)CoTaskMemAlloc(sizeof(*p));
        if (!p) return E_OUTOFMEMORY;
        p->dwFieldID = g_fields[dwIndex].dwFieldID;
        p->cpft = g_fields[dwIndex].cpft;
        p->guidFieldType = g_fields[dwIndex].guidFieldType;
        HRESULT hr = dupW(g_fields[dwIndex].pszLabel, &p->pszLabel);
        if (FAILED(hr)) {
            CoTaskMemFree(p);
            return hr;
        }
        *ppcpfd = p;
        return S_OK;
    }
    IFACEMETHODIMP GetCredentialCount(DWORD* pdwCount, DWORD* pdwDefault,
                                      BOOL* pbAutoLogonWithDefault) override {
        if (!pdwCount || !pdwDefault || !pbAutoLogonWithDefault) return E_POINTER;
        *pbAutoLogonWithDefault = FALSE;
        if (m_locked && m_pCred) {
            *pdwCount = 1;
            *pdwDefault = 0;
        } else {
            *pdwCount = 0;
            *pdwDefault = CREDENTIAL_PROVIDER_NO_DEFAULT;
        }
        return S_OK;
    }
    IFACEMETHODIMP GetCredentialAt(DWORD dwIndex, ICredentialProviderCredential** ppcpc) override {
        if (!ppcpc || dwIndex != 0 || !m_locked || !m_pCred) return E_INVALIDARG;
        return m_pCred->QueryInterface(__uuidof(ICredentialProviderCredential), (void**)ppcpc);
    }

    // ICredentialProviderFilter
    IFACEMETHODIMP Filter(CREDENTIAL_PROVIDER_USAGE_SCENARIO cpus, DWORD,
                          GUID* rgclsidProviders, BOOL* rgbAllow, DWORD cProviders) override {
        if (cpus != CPUS_LOGON && cpus != CPUS_UNLOCK_WORKSTATION) return S_OK;
        if (!lockstate::MarkerPresent()) return S_OK; // inert: allow everyone
        for (DWORD i = 0; i < cProviders; ++i) {
            rgbAllow[i] = IsEqualGUID(rgclsidProviders[i], CLSID_AutoDeployProvider) ? TRUE : FALSE;
        }
        return S_OK;
    }
    IFACEMETHODIMP UpdateRemoteCredential(const CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION*,
                                          CREDENTIAL_PROVIDER_CREDENTIAL_SERIALIZATION*) override {
        return E_NOTIMPL;
    }

  private:
    LONG m_cRef;
    CREDENTIAL_PROVIDER_USAGE_SCENARIO m_cpus = CPUS_INVALID;
    bool m_locked = false;
    CAutoDeployCredential* m_pCred = nullptr;
    ICredentialProviderEvents* m_pEvents = nullptr;
    UINT_PTR m_adviseCtx = 0;
};

// ---------------------------------------------------------------------------
// Class factory
// ---------------------------------------------------------------------------
class CClassFactory : public IClassFactory {
  public:
    CClassFactory() : m_cRef(1) {}
    virtual ~CClassFactory() {}
    IFACEMETHODIMP QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, IID_IClassFactory)) {
            *ppv = static_cast<IClassFactory*>(this);
            AddRef();
            return S_OK;
        }
        *ppv = nullptr;
        return E_NOINTERFACE;
    }
    IFACEMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&m_cRef); }
    IFACEMETHODIMP_(ULONG) Release() override {
        LONG n = InterlockedDecrement(&m_cRef);
        if (n == 0) delete this;
        return n;
    }
    IFACEMETHODIMP CreateInstance(IUnknown* pUnkOuter, REFIID riid, void** ppv) override {
        if (pUnkOuter) return CLASS_E_NOAGGREGATION;
        CAutoDeployProvider* p = new (std::nothrow) CAutoDeployProvider();
        if (!p) return E_OUTOFMEMORY;
        HRESULT hr = p->QueryInterface(riid, ppv);
        p->Release();
        return hr;
    }
    IFACEMETHODIMP LockServer(BOOL fLock) override {
        if (fLock) InterlockedIncrement(&g_cDllRef);
        else InterlockedDecrement(&g_cDllRef);
        return S_OK;
    }

  private:
    LONG m_cRef;
};

// ---------------------------------------------------------------------------
// Registration helpers
// ---------------------------------------------------------------------------
static LONG regSetString(HKEY root, const wchar_t* subkey, const wchar_t* name,
                         const wchar_t* value) {
    HKEY hk;
    LONG rc = RegCreateKeyExW(root, subkey, 0, nullptr, 0, KEY_WRITE, nullptr, &hk, nullptr);
    if (rc != ERROR_SUCCESS) return rc;
    rc = RegSetValueExW(hk, name, 0, REG_SZ, (const BYTE*)value,
                        (DWORD)((wcslen(value) + 1) * sizeof(wchar_t)));
    RegCloseKey(hk);
    return rc;
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------
extern "C" {

BOOL WINAPI DllMain(HINSTANCE hinst, DWORD reason, LPVOID) {
    if (reason == DLL_PROCESS_ATTACH) {
        g_hinst = hinst;
        DisableThreadLibraryCalls(hinst);
    }
    return TRUE;
}

HRESULT WINAPI DllCanUnloadNow() {
    return (InterlockedCompareExchange(&g_cDllRef, 0, 0) == 0) ? S_OK : S_FALSE;
}

HRESULT WINAPI DllGetClassObject(REFCLSID rclsid, REFIID riid, void** ppv) {
    if (!IsEqualCLSID(rclsid, CLSID_AutoDeployProvider)) return CLASS_E_CLASSNOTAVAILABLE;
    CClassFactory* f = new (std::nothrow) CClassFactory();
    if (!f) return E_OUTOFMEMORY;
    HRESULT hr = f->QueryInterface(riid, ppv);
    f->Release();
    return hr;
}

HRESULT WINAPI DllRegisterServer() {
    wchar_t module[MAX_PATH];
    if (!GetModuleFileNameW(g_hinst, module, MAX_PATH)) return HRESULT_FROM_WIN32(GetLastError());

    std::wstring clsidKey = std::wstring(L"CLSID\\") + kClsidStr;
    std::wstring inproc = clsidKey + L"\\InprocServer32";
    if (regSetString(HKEY_CLASSES_ROOT, clsidKey.c_str(), nullptr, kFriendly) != ERROR_SUCCESS)
        return E_FAIL;
    if (regSetString(HKEY_CLASSES_ROOT, inproc.c_str(), nullptr, module) != ERROR_SUCCESS)
        return E_FAIL;
    if (regSetString(HKEY_CLASSES_ROOT, inproc.c_str(), L"ThreadingModel", L"Apartment") != ERROR_SUCCESS)
        return E_FAIL;

    std::wstring cpKey =
        std::wstring(L"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Authentication\\"
                     L"Credential Providers\\") +
        kClsidStr;
    if (regSetString(HKEY_LOCAL_MACHINE, cpKey.c_str(), nullptr, kFriendly) != ERROR_SUCCESS)
        return E_FAIL;
    return S_OK;
}

HRESULT WINAPI DllUnregisterServer() {
    std::wstring clsidKey = std::wstring(L"CLSID\\") + kClsidStr;
    RegDeleteTreeW(HKEY_CLASSES_ROOT, clsidKey.c_str());
    std::wstring cpKey =
        std::wstring(L"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Authentication\\"
                     L"Credential Providers\\") +
        kClsidStr;
    RegDeleteTreeW(HKEY_LOCAL_MACHINE, cpKey.c_str());
    return S_OK;
}

} // extern "C"
