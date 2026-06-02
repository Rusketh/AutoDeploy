package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIPXEAutoexecNextServer(t *testing.T) {
	script := IPXEAutoexecNextServer("8080")
	for _, want := range []string{
		"#!ipxe",
		// Non-standard port is tried first.
		"chain --replace --autofree http://${next-server}:8080/ipxe/boot.ipxe ||",
		// Then plain :80 and HTTPS fallbacks.
		"chain --replace --autofree http://${next-server}/ipxe/boot.ipxe ||",
		"chain --replace --autofree https://${next-server}/ipxe/boot.ipxe ||",
		// Inherits the firmware lease rather than always re-DHCPing.
		"isset ${next-server} || dhcp ||",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n---\n%s", want, script)
		}
	}
}

func TestIPXEAutoexecNextServerPort80(t *testing.T) {
	// Port 80 (or empty) should not emit a redundant :80 attempt.
	for _, port := range []string{"80", ""} {
		script := IPXEAutoexecNextServer(port)
		if strings.Contains(script, "${next-server}:80/") {
			t.Errorf("port %q: unexpected explicit :80 attempt\n%s", port, script)
		}
		if !strings.Contains(script, "http://${next-server}/ipxe/boot.ipxe") {
			t.Errorf("port %q: missing default-port chain line", port)
		}
	}
}

func TestAutoexecHTTPRoute(t *testing.T) {
	mux := http.NewServeMux()
	RegisterIPXE(mux)

	req := httptest.NewRequest(http.MethodGet, "http://192.168.20.60:8080/autoexec.ipxe", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Errorf("body should start with #!ipxe shebang, got:\n%s", body)
	}
	// The HTTP-served variant chains straight to the server it was
	// fetched from — no ${next-server} indirection needed.
	if !strings.Contains(body, "chain --replace --autofree http://192.168.20.60:8080/ipxe/boot.ipxe") {
		t.Errorf("body missing host-specific chainload:\n%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestBootIPXEStillServed(t *testing.T) {
	mux := http.NewServeMux()
	RegisterIPXE(mux)

	req := httptest.NewRequest(http.MethodGet, "http://host:8080/ipxe/boot.ipxe", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "set base http://host:8080") {
		t.Errorf("boot.ipxe should set base from request host:\n%s", body)
	}
	// Secure Boot machines must register the signed shim before booting
	// the kernel; BIOS / SB-off machines fall through to the kernel.
	for _, want := range []string{
		"iseq ${efi/SecureBoot:int8} 1 || goto bootkernel",
		"shim ${base}/ipxe/static/autodeploy-shim.efi",
		":bootkernel",
		"kernel ${base}/ipxe/static/autodeploy-kernel",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("boot.ipxe missing %q\n%s", want, body)
		}
	}
}
