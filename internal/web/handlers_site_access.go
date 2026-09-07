package web

import (
	"net/http"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// RegisterSiteAccessRoutes keeps site access at both authorization layers: the
// account must manage all sites, and the loaded site must pass site assignment.
func (s *Server) RegisterSiteAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/access", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.siteAccessPage)))
	mux.HandleFunc("POST /sites/{id}/access", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.saveSiteAccess)))
}

func (s *Server) siteAccessPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	settings, err := s.store.SiteAccess(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load site access settings", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Site access", Section: "access", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, SiteAccess: &settings}
	if r.URL.Query().Get("apply") == "queued" {
		data.Message = "Access changes queued."
	}
	s.render(w, "site_access.html", data)
}

func (s *Server) saveSiteAccess(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	desired := model.SiteAccessSettings{
		SiteID:           site.ID,
		BasicAuthEnabled: r.FormValue("basic_auth_enabled") == "yes",
		Username:         strings.TrimSpace(r.FormValue("username")),
		CloudflareOnly:   r.FormValue("cloudflare_only") == "yes",
	}
	jobID, err := s.store.SetSiteAccess(r.Context(), user, site.ID, desired, r.FormValue("password"))
	if err != nil {
		current, _ := s.store.SiteAccess(r.Context(), site.ID)
		desired.PasswordHash = current.PasswordHash
		desired.Status, desired.LastError = current.Status, current.LastError
		s.renderStatus(w, "site_access.html", http.StatusBadRequest, pageData{Title: "Site access", Section: "access", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, SiteAccess: &desired, Error: err.Error()})
		return
	}
	returnURL := "/sites/" + site.ID + "/access"
	if r.Header.Get("X-WPX-Action") != "partial" {
		returnURL += "?apply=queued"
	}
	s.respondQueuedAction(w, r, user, jobID, returnURL, "Access changes")
}
