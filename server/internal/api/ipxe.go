package api

import (
	"fmt"
	"net/http"
	"strings"
)

// RegisterIPXE serves the two iPXE scripts AutoDeploy hands to network
// clients:
//
//   - /autoexec.ipxe — the bootstrap. STOCK, unmodified iPXE binaries
//     (boot.ipxe.org, distro packages, AutoDeploy's CI builds) probe
//     for this file automatically right after they come up, over the
//     same transport they booted from. Serving it means operators never
//     have to build a custom iPXE with a script embedded — the binary
//     stays generic and the server supplies the brains. The TFTP server
//     synthesises the same script for TFTP-booted clients (see
//     IPXEAutoexecNextServer); this HTTP route covers UEFI HTTP Boot,
//     where iPXE fetches autoexec.ipxe over HTTP from the URL it was
//     handed.
//
//   - /ipxe/boot.ipxe — the chainload target the bootstrap jumps to. It
//     loads the Linux pre-boot environment that runs autodeploy-boot.
//
// iPXE flow (TFTP / classic PXE):
//
//	iPXE firmware
//	  -> DHCP hands back a stock ipxe.efi via TFTP (next-server)
//	  -> ipxe.efi auto-fetches autoexec.ipxe from next-server (TFTP)
//	  -> autoexec.ipxe chains to http://<autodeploy>/ipxe/boot.ipxe
//	  -> boot.ipxe loads the kernel + initrd and boots them
//	  -> the initrd contains autodeploy-boot, which connects back to
//	     /api/v1/clients/menu and the rest of the API.
//
// The kernel and initrd files are served separately from the static
// /ipxe/ tree under AUTODEPLOY_DATA_DIR/ipxe/ — operators drop their
// built kernel and initrd there. The build script under
// scripts/initramfs/ shows how to construct an initrd that bundles
// autodeploy-boot.
func RegisterIPXE(mux *http.ServeMux) {
	mux.HandleFunc("GET /autoexec.ipxe", func(w http.ResponseWriter, r *http.Request) {
		// iPXE fetched this over HTTP, so we know exactly which server
		// it reached — point the chainload straight back at it rather
		// than relying on ${next-server} (which UEFI HTTP Boot doesn't
		// necessarily set).
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, ipxeAutoexecForBase(requestBase(r)))
	})

	mux.HandleFunc("GET /ipxe/boot.ipxe", func(w http.ResponseWriter, r *http.Request) {
		base := requestBase(r)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, `#!ipxe
# AutoDeploy chainload script.
#
# Chainloads the Linux pre-boot environment that runs autodeploy-boot.
# Place autodeploy-kernel and autodeploy-initrd under
# AUTODEPLOY_DATA_DIR/ipxe/ on the server.

set base %s
echo
echo AutoDeploy boot - chainloading client from ${base}
echo
kernel ${base}/ipxe/static/autodeploy-kernel console=ttyS0,115200 console=tty1 autodeploy.server=${base} autodeploy.uuid=${uuid}
initrd ${base}/ipxe/static/autodeploy-initrd
boot || goto fail

:fail
echo
echo Chainload failed - dropping to iPXE prompt.
shell
`, base)
	})
}

// requestBase returns scheme://host for the incoming request, honouring
// TLS termination at this server.
func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ipxeAutoexecForBase returns an autoexec.ipxe that chains straight to
// boot.ipxe on a known base URL (scheme://host[:port]). Used by the
// HTTP route, where the exact server is known from the request.
func ipxeAutoexecForBase(base string) string {
	return fmt.Sprintf(`#!ipxe
# AutoDeploy bootstrap (autoexec.ipxe, served over HTTP).
echo
echo === AutoDeploy bootstrap ===
echo iPXE ${version} on ${product}
echo
echo Chainloading %[1]s/ipxe/boot.ipxe ...
chain --replace --autofree %[1]s/ipxe/boot.ipxe ||
echo
echo Could not reach AutoDeploy at %[1]s - dropping to iPXE shell.
shell
`, base)
}

// IPXEAutoexecNextServer returns an autoexec.ipxe that resolves the
// AutoDeploy server from DHCP option 66 (${next-server}) and chains to
// boot.ipxe. This is what the built-in TFTP server hands to a stock
// iPXE binary that booted via classic PXE: the binary inherits the
// firmware's DHCP lease (so ${next-server} is already set — important
// on flaky synthetic NICs like Hyper-V Gen2, where iPXE's own re-DHCP
// fails) and never needs a URL baked in.
//
// httpPort is AutoDeploy's cleartext HTTP port (e.g. "8080"); pass ""
// when HTTP is disabled. The script tries that port first, then the
// standard :80 and finally HTTPS, so one file works regardless of how
// the operator exposed the server.
func IPXEAutoexecNextServer(httpPort string) string {
	var attempts strings.Builder
	if httpPort != "" && httpPort != "80" {
		fmt.Fprintf(&attempts,
			"chain --replace --autofree http://${next-server}:%s/ipxe/boot.ipxe ||\n", httpPort)
	}
	attempts.WriteString("chain --replace --autofree http://${next-server}/ipxe/boot.ipxe ||\n")
	attempts.WriteString("chain --replace --autofree https://${next-server}/ipxe/boot.ipxe ||\n")

	return fmt.Sprintf(`#!ipxe
# AutoDeploy bootstrap (autoexec.ipxe, served over TFTP).
:start
echo
echo === AutoDeploy bootstrap ===
echo iPXE ${version} on ${product}
# Inherit the firmware DHCP lease; only re-DHCP if next-server is unset.
# (Skipping the re-DHCP is what makes this work on Hyper-V Gen2's
# synthetic NIC, where iPXE's own dhcp call throws "Network unreachable".)
isset ${next-server} || dhcp ||
isset ${next-server} || goto noserver
echo Chainloading from ${next-server} ...
%sgoto failed
:noserver
echo
echo ERROR: DHCP did not provide next-server (option 66). Point your
echo        DHCP server's next-server / option 66 at the AutoDeploy host.
goto failed
:failed
echo
echo Could not reach AutoDeploy. From the iPXE shell you can run:
echo   chain http://YOUR-SERVER-IP:%s/ipxe/boot.ipxe
echo
shell
`, attempts.String(), defaultPortHint(httpPort))
}

// defaultPortHint picks a sensible port to show in the failure message.
func defaultPortHint(httpPort string) string {
	if httpPort != "" {
		return httpPort
	}
	return "8080"
}
