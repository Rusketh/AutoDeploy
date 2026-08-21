package portal

import "net/http"

// softwareImportPage renders the "Import from winget" page. The page is
// pure client-side JS against the /api/v1/winget/* and
// /api/v1/software/import endpoints — searching the winget community
// source, then downloading an app's real installer into a new, standard
// SoftwarePackage the operator can edit afterwards.
func softwareImportPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "software_import.html", "Import from winget", nil)
	}
}
