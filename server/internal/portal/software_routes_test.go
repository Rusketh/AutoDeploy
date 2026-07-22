package portal

import (
	"net/http"
	"testing"
)

// TestRegisterSoftwareRoutesNoPanic registers the software routes into a real
// net/http.ServeMux exactly the way Register does at startup. ServeMux panics
// when a "{name...}" trailing wildcard isn't the final path segment, so this
// reproduces the startup path the direct-handler tests skip -- a bad route
// pattern would panic here instead of only in production.
func TestRegisterSoftwareRoutesNoPanic(t *testing.T) {
	mux := http.NewServeMux()
	get := func(path string, h http.HandlerFunc) { mux.HandleFunc("GET "+path, h) }
	post := func(path string, h http.HandlerFunc) { mux.HandleFunc("POST "+path, h) }

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering software routes panicked: %v", r)
		}
	}()
	registerSoftwareRoutes(get, post, Repos{})
}
