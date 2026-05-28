package api

import (
	"fmt"
	"net/http"
)

// RegisterIPXE serves an iPXE script that chainloads the AutoDeploy Boot
// Client over HTTP. The script is parameterised by the request's host so
// the chainload URL points back at the same server the iPXE client reached.
//
// iPXE flow:
//   iPXE firmware
//     -> DHCP server hands back http://<autodeploy>/ipxe/boot.ipxe
//     -> this endpoint returns the script below
//     -> the script loads the Linux kernel + initrd and boots them
//     -> the initrd contains autodeploy-boot, which connects back to
//        /api/v1/clients/menu and the rest of the API.
//
// The kernel and initrd files are served separately from the static
// /ipxe/ tree under AUTODEPLOY_DATA_DIR/ipxe/ — operators drop their
// built kernel and initrd there. The build script under
// scripts/initramfs/ shows how to construct an initrd that bundles
// autodeploy-boot.
func RegisterIPXE(mux *http.ServeMux) {
	mux.HandleFunc("GET /ipxe/boot.ipxe", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base := scheme + "://" + r.Host
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
kernel ${base}/ipxe/static/autodeploy-kernel console=tty1 console=ttyS0,115200 autodeploy.server=${base} autodeploy.uuid=${uuid}
initrd ${base}/ipxe/static/autodeploy-initrd
boot || goto fail

:fail
echo
echo Chainload failed - dropping to iPXE prompt.
shell
`, base)
	})
}
