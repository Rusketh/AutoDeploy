package portal

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerImageRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/images", imageList(r))
		get("/portal/images/new", imageFormNew(r))
		post("/portal/images", imageCreate(r))
		get("/portal/images/{id}/edit", imageEdit(r))
		post("/portal/images/{id}", imageUpdate(r))
		post("/portal/images/{id}/delete", imageDelete(r))
		post("/portal/images/{id}/clone", imageClone(r))
		get("/portal/images/{id}/resolved", imageResolved(r))
		// Image group management — sub-page linked from the images list.
		get("/portal/images/groups", imageGroupList(r))
		post("/portal/images/groups", imageGroupCreate(r))
		post("/portal/images/groups/{id}/save", imageGroupUpdate(r))
		post("/portal/images/groups/{id}/delete", imageGroupDelete(r))
	}
}

func imageList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		images, err := r.Images.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Build a name map for ISO / Unattend / Loadout / parent image so
		// the list shows names instead of bare IDs.
		isoNames := map[model.ID]string{}
		if isos, _ := r.ISOs.List(req.Context()); isos != nil {
			for _, i := range isos {
				isoNames[i.ID] = i.Name
			}
		}
		uaNames := map[model.ID]string{}
		for _, u := range mustListUnattend(r, req) {
			uaNames[u.ID] = u.Name
		}
		ldNames := map[model.ID]string{}
		for _, l := range mustListLoadout(r, req) {
			ldNames[l.ID] = l.Name
		}
		imgNames := map[model.ID]string{}
		for _, im := range images {
			imgNames[im.ID] = im.Name
		}
		groups, _ := mustListImageGroups(r, req)
		grpNames := map[model.ID]string{}
		grpRefs := map[model.ID]int{}
		for _, g := range groups {
			grpNames[g.ID] = g.Name
			if r.ImageGroups != nil {
				n, _ := r.ImageGroups.RefCount(req.Context(), g.ID)
				grpRefs[g.ID] = n
			}
		}
		render(w, req, r, "image_list.html", "Images", map[string]any{
			"Images":   images,
			"ISONames": isoNames, "UnattendNames": uaNames,
			"LoadoutNames": ldNames, "ImageNames": imgNames,
			"Groups": groups, "GroupNames": grpNames, "GroupRefs": grpRefs,
		})
	}
}

func mustListUnattend(r Repos, req *http.Request) []model.Unattend {
	v, _ := r.Unattend.List(req.Context())
	return v
}
func mustListLoadout(r Repos, req *http.Request) []model.SoftwareLoadout {
	v, _ := r.Loadouts.List(req.Context())
	return v
}
func mustListImageGroups(r Repos, req *http.Request) ([]model.ImageGroup, error) {
	if r.ImageGroups == nil {
		return nil, nil
	}
	return r.ImageGroups.List(req.Context())
}

func imageFormNew(r Repos) http.HandlerFunc {
	return imageFormFor(r, model.Image{}, true)
}

func imageFormFor(r Repos, im model.Image, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		isos, _ := r.ISOs.List(req.Context())
		unattends, _ := r.Unattend.List(req.Context())
		loadouts, _ := r.Loadouts.List(req.Context())
		images, _ := r.Images.List(req.Context())
		packages, _ := r.Software.List(req.Context())
		title := "New image"
		if !isNew {
			title = "Edit image: " + im.Name
		}
		var dj model.ImageDomainJoin
		if !isNew && r.DomainJoin != nil {
			dj, _ = r.DomainJoin.Get(req.Context(), im.ID)
		}
		var sl model.ImageSetupLock
		if !isNew && r.SetupLock != nil {
			sl, _ = r.SetupLock.Get(req.Context(), im.ID)
		}
		groups, _ := mustListImageGroups(r, req)
		render(w, req, r, "image_form.html", title, map[string]any{
			"Im": im, "IsNew": isNew,
			"ISOs": isos, "Unattends": unattends, "Loadouts": loadouts,
			"Images": images, "Packages": packages,
			"DomainJoin": dj,
			"SetupLock":  sl,
			"Groups":     groups,
		})
	}
}

func imageEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		im, err := r.Images.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		imageFormFor(r, im, false)(w, req)
	}
}

func buildImageFromForm(req *http.Request) (model.Image, error) {
	if err := req.ParseForm(); err != nil {
		return model.Image{}, err
	}
	im := model.Image{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
	}
	setIDPtr := func(field string, dst **model.ID) {
		v := strings.TrimSpace(req.FormValue(field))
		if v == "" || v == "0" {
			*dst = nil
			return
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			id := model.ID(n)
			*dst = &id
		}
	}
	setIDPtr("parent_id", &im.ParentID)
	setIDPtr("iso_id", &im.ISOID)
	setIDPtr("unattend_id", &im.UnattendID)
	setIDPtr("loadout_id", &im.LoadoutID)
	setIDPtr("group_id", &im.GroupID)
	ids := req.Form["sw_id[]"]
	orders := req.Form["sw_order[]"]
	for i, raw := range ids {
		pid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		ord := int64(100)
		if i < len(orders) {
			if n, err := strconv.ParseInt(strings.TrimSpace(orders[i]), 10, 64); err == nil {
				ord = n
			}
		}
		im.SoftwareLinks = append(im.SoftwareLinks, model.ImageSoftwareLink{
			PackageID: model.ID(pid), OrderValue: ord,
		})
	}
	return im, nil
}

func imageCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		im, err := buildImageFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/images/new", http.StatusFound)
			return
		}
		out, err := r.Images.Create(req.Context(), im)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/images/new", http.StatusFound)
			return
		}
		if err := saveDomainJoinFromForm(r, req, out.ID); err != nil {
			flash(w, "err", "Image created, but domain-join config failed: "+err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", out.ID), http.StatusFound)
			return
		}
		if err := saveSetupLockFromForm(r, req, out.ID); err != nil {
			flash(w, "err", "Image created, but setup-lock config failed: "+err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", out.ID), http.StatusFound)
			return
		}
		flash(w, "ok", "Image created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", out.ID), http.StatusFound)
	}
}

func imageUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		im, err := buildImageFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", id), http.StatusFound)
			return
		}
		im.ID = id
		if err := r.Images.Update(req.Context(), im); err != nil {
			flash(w, "err", err.Error())
		} else if err := saveDomainJoinFromForm(r, req, id); err != nil {
			flash(w, "err", "domain-join config: "+err.Error())
		} else if err := saveSetupLockFromForm(r, req, id); err != nil {
			flash(w, "err", "setup-lock config: "+err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", id), http.StatusFound)
	}
}

// saveDomainJoinFromForm persists the agent-driven AD join config from the
// image edit form. The password field is write-only: a blank value keeps the
// stored secret. No-op when the repo isn't wired.
func saveDomainJoinFromForm(r Repos, req *http.Request, imageID model.ID) error {
	if r.DomainJoin == nil {
		return nil
	}
	cfg := model.ImageDomainJoin{
		ImageID:  imageID,
		Enabled:  req.FormValue("dj_enabled") == "1",
		Domain:   strings.TrimSpace(req.FormValue("dj_domain")),
		OU:       strings.TrimSpace(req.FormValue("dj_ou")),
		JoinUser: strings.TrimSpace(req.FormValue("dj_user")),
	}
	return r.DomainJoin.Set(req.Context(), cfg, req.FormValue("dj_password"))
}

// saveSetupLockFromForm persists the per-image setup-lockout toggle from the
// image edit form. No-op when the repo isn't wired.
func saveSetupLockFromForm(r Repos, req *http.Request, imageID model.ID) error {
	if r.SetupLock == nil {
		return nil
	}
	return r.SetupLock.Set(req.Context(), model.ImageSetupLock{
		ImageID: imageID,
		Enabled: req.FormValue("setuplock_enabled") == "1",
	})
}

func imageDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Images.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/images", http.StatusFound)
	}
}

func imageClone(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		src, err := r.Images.Get(req.Context(), id)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/images", http.StatusFound)
			return
		}
		newName := "Copy of " + src.Name
		out, err := r.Images.Clone(req.Context(), id, newName)
		if err != nil {
			flash(w, "err", "Clone failed: "+err.Error())
			http.Redirect(w, req, "/portal/images", http.StatusFound)
			return
		}

		// Copy domain-join config (including the encrypted password).
		if r.DomainJoin != nil {
			if dj, err := r.DomainJoin.Get(req.Context(), id); err == nil {
				pw, _ := r.DomainJoin.RetrievePassword(req.Context(), id)
				dj.ImageID = out.ID
				_ = r.DomainJoin.Set(req.Context(), dj, pw)
			}
		}

		// Copy setup-lock toggle.
		if r.SetupLock != nil {
			if sl, err := r.SetupLock.Get(req.Context(), id); err == nil {
				sl.ImageID = out.ID
				_ = r.SetupLock.Set(req.Context(), sl)
			}
		}

		flash(w, "ok", fmt.Sprintf("Cloned %q — rename and adjust as needed.", src.Name))
		http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", out.ID), http.StatusFound)
	}
}

func imageResolved(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		res, err := r.Resolver.Resolve(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "image_resolved.html", "Resolved configuration", map[string]any{
			"R": res,
		})
	}
}

func imageGroupList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		groups, _ := mustListImageGroups(r, req)
		grpRefs := map[model.ID]int{}
		if r.ImageGroups != nil {
			for _, g := range groups {
				n, _ := r.ImageGroups.RefCount(req.Context(), g.ID)
				grpRefs[g.ID] = n
			}
		}
		render(w, req, r, "image_group_list.html", "Image Groups", map[string]any{
			"Groups":   groups,
			"GroupRefs": grpRefs,
		})
	}
}

func imageGroupCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.ImageGroups == nil {
			http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
			return
		}
		if err := req.ParseForm(); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
			return
		}
		g := model.ImageGroup{
			Name:        strings.TrimSpace(req.FormValue("name")),
			Description: strings.TrimSpace(req.FormValue("description")),
		}
		if _, err := r.ImageGroups.Create(req.Context(), g); err != nil {
			flash(w, "err", "Create group failed: "+err.Error())
		} else {
			flash(w, "ok", fmt.Sprintf("Group %q created.", g.Name))
		}
		http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
	}
}

func imageGroupUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.ImageGroups == nil {
			http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
			return
		}
		id, _ := pathID(req)
		if err := req.ParseForm(); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
			return
		}
		g := model.ImageGroup{
			ID:          id,
			Name:        strings.TrimSpace(req.FormValue("name")),
			Description: strings.TrimSpace(req.FormValue("description")),
		}
		if err := r.ImageGroups.Update(req.Context(), g); err != nil {
			flash(w, "err", "Update group failed: "+err.Error())
		} else {
			flash(w, "ok", fmt.Sprintf("Group %q saved.", g.Name))
		}
		http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
	}
}

func imageGroupDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.ImageGroups == nil {
			http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
			return
		}
		id, _ := pathID(req)
		if err := r.ImageGroups.Delete(req.Context(), id); err != nil {
			flash(w, "err", "Delete group failed: "+err.Error())
		} else {
			flash(w, "ok", "Group deleted.")
		}
		http.Redirect(w, req, "/portal/images/groups", http.StatusFound)
	}
}
