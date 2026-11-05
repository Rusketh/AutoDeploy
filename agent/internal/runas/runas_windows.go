//go:build windows

package runas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modadvapi32          = windows.NewLazySystemDLL("advapi32.dll")
	moduserenv           = windows.NewLazySystemDLL("userenv.dll")
	procLogonUserW       = modadvapi32.NewProc("LogonUserW")
	procLoadUserProfileW = moduserenv.NewProc("LoadUserProfileW")
	procUnloadUserProf   = moduserenv.NewProc("UnloadUserProfile")
)

const (
	logon32LogonInteractive = 2
	logon32LogonBatch       = 4
	logon32ProviderDefault  = 0
	errorLogonTypeNotGrant  = windows.Errno(1385) // ERROR_LOGON_TYPE_NOT_GRANTED
	piNoUI                  = 1
	swHide                  = 0
)

// profileInfo mirrors PROFILEINFOW.
type profileInfo struct {
	Size        uint32
	Flags       uint32
	UserName    *uint16
	ProfilePath *uint16
	DefaultPath *uint16
	ServerName  *uint16
	PolicyPath  *uint16
	Profile     windows.Handle
}

// Session is a logged-on account the agent can start processes as.
type Session struct {
	// Account is the account name, for logs and results (never a secret).
	Account string

	token   windows.Token
	profile windows.Handle
	env     []string
	sid     string
	// desktop is "" for a non-interactive logon (the system gives the
	// process its own hidden window station) or winsta0\default for the
	// signed-in user's session.
	desktop string
}

// Logon logs account on with password (a batch logon, falling back to an
// interactive logon where batch isn't granted), loads its profile so HKCU and
// %APPDATA% work, and returns a Session. An administrator's UAC-filtered token
// is swapped for its elevated linked token, so the account runs with the
// rights an operator expects. The password is used once and not retained.
func Logon(account, password string) (*Session, error) {
	domain, user := SplitAccount(account)
	tok, err := logonUser(user, domain, password, logon32LogonBatch)
	if errors.Is(err, errorLogonTypeNotGrant) {
		tok, err = logonUser(user, domain, password, logon32LogonInteractive)
	}
	if err != nil {
		return nil, fmt.Errorf("log on as %s: %w", account, err)
	}
	if !tok.IsElevated() {
		if linked, lerr := tok.GetLinkedToken(); lerr == nil {
			var primary windows.Token
			derr := windows.DuplicateTokenEx(linked, windows.MAXIMUM_ALLOWED, nil,
				windows.SecurityImpersonation, windows.TokenPrimary, &primary)
			linked.Close()
			if derr == nil {
				tok.Close()
				tok = primary
			}
		}
	}
	s := &Session{Account: account, token: tok}
	if err := s.loadProfile(user); err != nil {
		s.Close()
		return nil, fmt.Errorf("load the profile of %s: %w", account, err)
	}
	if err := s.init(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// LoggedOnUser returns a Session for the user signed in at the console (or,
// failing that, the first active remote session). The token is the user's
// normal, non-elevated one: the kit runs with the rights that user has.
func LoggedOnUser() (*Session, error) {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	var tok windows.Token
	err := errors.New("no console session")
	if sessionID != 0xFFFFFFFF {
		err = windows.WTSQueryUserToken(sessionID, &tok)
	}
	if err != nil {
		tok, err = activeSessionToken()
	}
	if err != nil {
		return nil, ErrNoInteractiveUser
	}
	s := &Session{token: tok, desktop: `winsta0\default`}
	if err := s.init(); err != nil {
		s.Close()
		return nil, err
	}
	if u, err := tok.GetTokenUser(); err == nil {
		if acct, dom, _, lerr := u.User.Sid.LookupAccount(""); lerr == nil {
			s.Account = dom + `\` + acct
		}
	}
	if s.Account == "" {
		s.Account = s.sid
	}
	return s, nil
}

func activeSessionToken() (windows.Token, error) {
	var infos *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &infos, &count); err != nil {
		return 0, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(infos)))
	for _, si := range unsafe.Slice(infos, count) {
		if si.State != windows.WTSActive {
			continue
		}
		var tok windows.Token
		if err := windows.WTSQueryUserToken(si.SessionID, &tok); err == nil {
			return tok, nil
		}
	}
	return 0, ErrNoInteractiveUser
}

func logonUser(user, domain, password string, logonType uint32) (windows.Token, error) {
	u, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return 0, err
	}
	var d *uint16
	if domain != "" {
		if d, err = windows.UTF16PtrFromString(domain); err != nil {
			return 0, err
		}
	}
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return 0, err
	}
	var tok windows.Token
	r, _, e := procLogonUserW.Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)),
		uintptr(unsafe.Pointer(p)), uintptr(logonType), logon32ProviderDefault,
		uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		if e == nil || e == windows.Errno(0) {
			e = errors.New("LogonUser failed")
		}
		return 0, e
	}
	return tok, nil
}

func (s *Session) loadProfile(user string) error {
	name, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	pi := profileInfo{Flags: piNoUI, UserName: name}
	pi.Size = uint32(unsafe.Sizeof(pi))
	r, _, e := procLoadUserProfileW.Call(uintptr(s.token), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		return e
	}
	s.profile = pi.Profile
	return nil
}

func (s *Session) init() error {
	env, err := s.token.Environ(false)
	if err != nil {
		return fmt.Errorf("build the environment for the run-as account: %w", err)
	}
	s.env = env
	u, err := s.token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read the run-as account's SID: %w", err)
	}
	s.sid = u.User.Sid.String()
	return nil
}

// Close unloads the profile and releases the token.
func (s *Session) Close() {
	if s == nil {
		return
	}
	if s.profile != 0 {
		_, _, _ = procUnloadUserProf.Call(uintptr(s.token), uintptr(s.profile))
		s.profile = 0
	}
	if s.token != 0 {
		_ = s.token.Close()
		s.token = 0
	}
}

// Expand expands %VARS% as the account sees them (its profile paths, TEMP...).
func (s *Session) Expand(str string) string { return ExpandWith(s.env, str) }

// GrantDir gives the account modify rights on dir (and everything under it),
// so its steps can read the kit's files and write logs beside them.
func (s *Session) GrantDir(dir string) error {
	out, err := exec.Command("icacls", dir, "/grant", "*"+s.sid+":(OI)(CI)M", "/T", "/C", "/Q").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Launch starts name with args as the session's account and waits for it.
// The process tree is placed in a job object so a cancelled ctx (timeout)
// terminates it entirely. Output is copied to stdout/stderr.
func (s *Session) Launch(ctx context.Context, name string, args []string, dir, stdin string, stdout, stderr io.Writer) (int, error) {
	path := name
	if !filepath.IsAbs(name) && !strings.ContainsAny(name, `\/`) {
		if p, err := exec.LookPath(name); err == nil {
			path = p
		}
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	if err != nil {
		return -1, err
	}
	var dirPtr *uint16
	if dir != "" {
		if dirPtr, err = windows.UTF16PtrFromString(dir); err != nil {
			return -1, err
		}
	}
	desktop := []uint16{0} // "" -> a private, non-interactive window station
	if s.desktop != "" {
		desktop = windows.StringToUTF16(s.desktop)
	}
	env := envBlock(s.env)

	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	var outR, outW, errR, errW, inR, inW windows.Handle
	if err := windows.CreatePipe(&outR, &outW, sa, 0); err != nil {
		return -1, err
	}
	if err := windows.CreatePipe(&errR, &errW, sa, 0); err != nil {
		closeAll(outR, outW)
		return -1, err
	}
	if err := windows.CreatePipe(&inR, &inW, sa, 0); err != nil {
		closeAll(outR, outW, errR, errW)
		return -1, err
	}
	// The parent's ends must not leak into the child.
	for _, h := range []windows.Handle{outR, errR, inW} {
		_ = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0)
	}

	si := windows.StartupInfo{
		Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
		ShowWindow: swHide,
		Desktop:    &desktop[0],
		StdInput:   inR,
		StdOutput:  outW,
		StdErr:     errW,
	}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	err = windows.CreateProcessAsUser(s.token, nil, cmdline, nil, nil, true, flags,
		&env[0], dirPtr, &si, &pi)
	closeAll(outW, errW, inR) // the child holds its own copies now
	if err != nil {
		closeAll(outR, errR, inW)
		return -1, fmt.Errorf("start %s as %s: %w", name, s.Account, err)
	}
	defer closeAll(pi.Process, pi.Thread)

	job, jerr := windows.CreateJobObject(nil, nil)
	if jerr == nil {
		if aerr := windows.AssignProcessToJobObject(job, pi.Process); aerr != nil {
			_ = windows.CloseHandle(job)
			job = 0
		}
	}
	if job != 0 {
		defer windows.CloseHandle(job)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		closeAll(outR, errR, inW)
		return -1, fmt.Errorf("resume %s: %w", name, err)
	}

	// stdin, then close it so the child sees EOF.
	go func() {
		f := os.NewFile(uintptr(inW), "stdin")
		if stdin != "" {
			_, _ = io.WriteString(f, stdin)
		}
		_ = f.Close()
	}()
	var wg sync.WaitGroup
	outF, errF := os.NewFile(uintptr(outR), "stdout"), os.NewFile(uintptr(errR), "stderr")
	for _, p := range []struct {
		r *os.File
		w io.Writer
	}{{outF, stdout}, {errF, stderr}} {
		wg.Add(1)
		go func(r *os.File, w io.Writer) {
			defer wg.Done()
			if w == nil {
				w = io.Discard
			}
			_, _ = io.Copy(w, r)
		}(p.r, p.w)
	}

	exited := make(chan struct{})
	go func() {
		_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		close(exited)
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		if job != 0 {
			_ = windows.TerminateJobObject(job, 1)
		} else {
			_ = windows.TerminateProcess(pi.Process, 1)
		}
		<-exited
	}
	// A grandchild that inherited the pipes can keep them open after the
	// main process exits; don't wait on it forever.
	copied := make(chan struct{})
	go func() { wg.Wait(); close(copied) }()
	select {
	case <-copied:
	case <-time.After(3 * time.Second):
		_ = outF.Close()
		_ = errF.Close()
		<-copied
	}
	_ = outF.Close()
	_ = errF.Close()

	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return -1, err
	}
	if ctx.Err() != nil {
		return int(code), ctx.Err()
	}
	return int(code), nil
}

func closeAll(hs ...windows.Handle) {
	for _, h := range hs {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
}
