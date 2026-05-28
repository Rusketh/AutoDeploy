package api

import (
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/branding"
)

// RegisterBranding mounts the branding endpoints. GET is open (the
// portal renders the brand even before login, the boot screen needs it,
// and deployed machines fetch it for OEM info). PUT requires a session.
func RegisterBranding(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/branding", handleGetBrand(r))
	mux.HandleFunc("PUT /api/v1/branding", handlePutBrand(r))
}

func handleGetBrand(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, err := r.Branding.Get(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func handlePutBrand(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in branding.Brand
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := r.Branding.Set(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		b, _ := r.Branding.Get(req.Context())
		writeJSON(w, http.StatusOK, b)
	}
}
