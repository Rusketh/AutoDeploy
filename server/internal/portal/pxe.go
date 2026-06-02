package portal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pxePageData drives the Settings -> PXE central hub. The page
// shows:
//   - the iPXE binaries the server is currently serving via TFTP
//     (filename, size, sha256, last-modified, download button)
//   - per-router DHCP setup snippets with the server's actual
//     IP filled in, copy-to-clipboard friendly
//   - a brief boot-flow diagram and verification recipe
//
// Operators landing here for the first time should be able to set
// up PXE end-to-end without leaving the page.
type pxePageData struct {
	// Binaries currently in $DATA_DIR/ipxe/. Each row flags its
	// Secure Boot role (Microsoft-signed shim, iPXE-CA-signed binary,
	// or a legacy BIOS NBP for which Secure Boot does not apply).
	Binaries []pxeBinary

	// IPXEDir is the resolved path on disk (per-category overrides
	// are honoured). Shown so operators can verify where to drop
	// hand-built replacements.
	IPXEDir string

	// ServerIP is the operator-facing IP the DHCP snippets pre-fill.
	// Best-effort: the host's primary outbound IP. Operators on
	// multi-NIC boxes can override the snippets manually.
	ServerIP string
	// HTTPPort is the AutoDeploy HTTP listener port the snippets
	// reference for the iPXE boot script URL (defaults to 8080;
	// empty when HTTPS-only).
	HTTPPort string
	// HTTPSPort is parallel to HTTPPort when an HTTPS listener
	// is configured.
	HTTPSPort string

	// FetchHelperPath is the path on disk to fetch-ipxe.sh (so the
	// "redownload" hint lines up with what the install script
	// actually deployed). Empty when not found.
	FetchHelperPath string

	// TFTPListening reports whether the AutoDeploy TFTP listener
	// is configured. The Network Boot / Bootfile DHCP option
	// only works when TFTP is up; the page nags if not.
	TFTPListening bool
}

type pxeBinary struct {
	Name     string
	Path     string
	Size     int64
	SHA256   string
	Modified time.Time
	// SecureBoot classifies the file's role in the UEFI Secure Boot
	// chain, for the portal table. One of:
	//   "shim"   — a Microsoft-signed shim; hand this out as the DHCP
	//              bootfile on Secure Boot machines.
	//   "signed" — an iPXE binary signed with the iPXE CA; the shim
	//              verifies and loads it (and the firmware loads it
	//              directly when Secure Boot is off).
	//   "n/a"    — legacy BIOS NBP; BIOS has no Secure Boot.
	//   ""       — unrecognised.
	SecureBoot string
	// Purpose is the short human-readable description we put next
	// to the filename ("legacy BIOS", "UEFI x64", "UEFI x64 (SNP
	// fallback)", "UEFI arm64"). Populated from the filename;
	// "Unknown" for binaries we don't recognise.
	Purpose string
}

// pxePage renders the central PXE hub. Reachable from
// Settings -> PXE.
func pxePage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		data := pxePageData{}
		data.IPXEDir = pxeIPXEDir(r)
		data.Binaries = scanIPXEDir(data.IPXEDir)
		data.ServerIP = guessPrimaryIP(req)
		data.HTTPPort = pxeHTTPPort(r)
		data.HTTPSPort = pxeHTTPSPort(r)
		data.TFTPListening = pxeTFTPConfigured(r)
		// fetch-ipxe.sh is installed alongside the autodeploy-update
		// helper. Probe both common locations so the page mentions
		// the actual path the operator can paste.
		for _, candidate := range []string{
			"/usr/local/sbin/autodeploy-fetch-ipxe",
			"/etc/autodeploy/scripts/fetch-ipxe.sh",
			"/opt/autodeploy/scripts/fetch-ipxe.sh",
		} {
			if _, err := os.Stat(candidate); err == nil {
				data.FetchHelperPath = candidate
				break
			}
		}
		render(w, req, r, "settings_pxe.html", "PXE setup", data)
	}
}

// pxeDownload streams one iPXE binary back to the operator. URL
// pattern: GET /portal/settings/pxe/download/{name}. The name is
// sanitised against the same rules the upload handler uses, then
// joined against the resolved iPXE directory.
func pxeDownload(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			http.Error(w, "bad filename", http.StatusBadRequest)
			return
		}
		dir := pxeIPXEDir(r)
		abs := filepath.Join(dir, name)
		f, err := os.Open(abs)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"`, name))
		http.ServeContent(w, req, name, info.ModTime(), f)
	}
}

// pxeIPXEDir returns the on-disk path the server is serving iPXE
// binaries from. Mirrors the resolution the TFTP listener does.
func pxeIPXEDir(r Repos) string {
	if r.Blobs != nil {
		return r.Blobs.CategoryRoot("ipxe")
	}
	return ""
}

// scanIPXEDir lists the iPXE binaries in dir. Files that don't look
// like iPXE (anything other than the well-known names) are skipped
// so a manually-dropped autoexec.ipxe doesn't appear in the table.
func scanIPXEDir(dir string) []pxeBinary {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	// known maps each filename AutoDeploy serves to its human-readable
	// purpose and Secure Boot role. These come from the official signed
	// iPXE release (ipxeboot.tar.gz) fetched by scripts/fetch-ipxe.sh.
	type info struct{ purpose, secureBoot string }
	known := map[string]info{
		"undionly.kpxe":    {"Legacy BIOS PXE", "n/a"},
		"ipxe.pxe":         {"Legacy BIOS (NBP variant, fallback for flaky NICs)", "n/a"},
		"ipxe.efi":         {"UEFI x64 (iPXE; loaded directly when Secure Boot is off)", "signed"},
		"snponly.efi":      {"UEFI x64 (SNP-only driver, fallback for Hyper-V / some NICs)", "signed"},
		"shimx64.efi":      {"UEFI x64 Secure Boot shim → ipxe.efi", "shim"},
		"ipxe-shim.efi":    {"UEFI x64 Secure Boot shim → ipxe.efi (hand this out for Secure Boot)", "shim"},
		"snponly-shim.efi": {"UEFI x64 Secure Boot shim → snponly.efi", "shim"},
		"ipxe-arm64.efi":   {"UEFI arm64 (iPXE; arm64 Secure Boot trio lives in arm64-sb/)", "signed"},
	}
	var out []pxeBinary
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		meta, ok := known[e.Name()]
		if !ok {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		out = append(out, pxeBinary{
			Name:       e.Name(),
			Path:       full,
			Size:       fi.Size(),
			Modified:   fi.ModTime(),
			SHA256:     pxeFileSHA256Hex(full),
			SecureBoot: meta.secureBoot,
			Purpose:    meta.purpose,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pxeFileSHA256Hex computes the SHA-256 of a file and returns the
// hex digest. Empty on read error; the page renders "—" for empty.
func pxeFileSHA256Hex(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// guessPrimaryIP returns the operator's IP we use to pre-fill the
// DHCP snippets. We use the local end of a UDP socket "connected"
// to 1.1.1.1 -- no traffic is sent, the kernel just picks the
// right interface. Falls back to the Host header if the trick
// fails.
func guessPrimaryIP(req *http.Request) string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return "YOUR-SERVER-IP"
}

// pxeHTTPPort / pxeHTTPSPort extract just the port from the
// configured bind addresses. Empty when the listener isn't
// configured. The portal config holds these as
// AUTODEPLOY_HTTP_ADDR / HTTPS_ADDR, but Repos doesn't currently
// surface them -- read directly from the env so the snippet pre-
// fills the right port without a Repos schema change.
func pxeHTTPPort(_ Repos) string {
	return portFromEnv("AUTODEPLOY_HTTP_ADDR")
}

func pxeHTTPSPort(_ Repos) string {
	return portFromEnv("AUTODEPLOY_HTTPS_ADDR")
}

func portFromEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return ""
	}
	return port
}

// pxeTFTPConfigured returns true when AUTODEPLOY_TFTP_ADDR is set.
// The page nags when it isn't, since "set up PXE" without TFTP
// requires the operator to run a separate TFTP daemon.
func pxeTFTPConfigured(_ Repos) bool {
	return os.Getenv("AUTODEPLOY_TFTP_ADDR") != ""
}
