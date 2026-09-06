package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// PHP switches are durable host jobs. The HTTP request never waits for package
// installation or changes the recorded current version before the job succeeds.
func (s *Server) changePHPVersion(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if _, err := s.store.EnqueuePHPVersionChange(r.Context(), user, site.ID, r.FormValue("php_version"), r.FormValue("allow_eol") == "yes"); err != nil {
		snippets, loadErr := s.store.SiteSnippets(r.Context(), site.ID)
		data := pageData{Title: "Site settings", Section: "settings", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, Error: err.Error(), Form: map[string]string{"php_version": r.FormValue("php_version"), "allow_eol": r.FormValue("allow_eol")}}
		if loadErr == nil {
			data.SiteSnippets = &snippets
		}
		s.renderStatus(w, "site.html", http.StatusBadRequest, data)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/settings?php=queued", http.StatusSeeOther)
}
