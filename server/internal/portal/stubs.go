package portal

import "net/http"

// stubs.go is the temporary scaffolding for portal entities under
// active build-out. Each stub renders a "coming soon" placeholder
// until its dedicated file replaces the registrar.

func placeholder(r Repos, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "placeholder.html", title, map[string]any{"Title": title})
	}
}

// Each registerXxxRoutes below is overridden by a dedicated file.
// Declared here as a fallback so portal.go compiles in any
// intermediate state.

var registerUnattendRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/unattends", placeholder(r, "Unattends"))
}
var registerDriverRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/drivers", placeholder(r, "Driver packages"))
}
var registerSoftwareRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/software", placeholder(r, "Software packages"))
}
var registerLoadoutRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/loadouts", placeholder(r, "Software loadouts"))
}
var registerImageRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/images", placeholder(r, "Images"))
}
var registerInventoryRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/machines", placeholder(r, "Machines"))
}
var registerMachineGroupRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/groups", placeholder(r, "Machine groups"))
}
var registerBulkRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/bulk", placeholder(r, "Bulk operations"))
}
var registerLogsRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/logs", placeholder(r, "Logs"))
}
var registerSettingsRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/settings", placeholder(r, "Settings"))
}
var registerImportRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
	get("/portal/import", placeholder(r, "Import assets"))
}
