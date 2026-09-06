package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// A domain is an editable address, not the site's identity. Queue the change
// against its immutable ID; the worker commits the new address only after host
// reconciliation succeeds. A retry reuses that operation's recovery journal.
func (s *Server) changeSiteDomain(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueDomainChange(r.Context(), user, site.ID, r.FormValue("domain")); err != nil {
		s.renderLifecycleError(w, r, user, site, err.Error())
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/settings?domain=queued", http.StatusSeeOther)
}

func (s *Server) retrySiteDomain(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if _, err := s.store.RetryDomainChange(r.Context(), user, site.ID); err != nil {
		s.renderLifecycleError(w, r, user, site, err.Error())
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/settings?domain=queued", http.StatusSeeOther)
}

// Deletion is deliberately narrower than ordinary site administration. Both
// this boundary and the store check owner authority, and the store compares the
// typed domain with current state before reserving the destructive operation.
func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if user.Role != rbac.Owner || !s.validCSRF(r) {
		http.Error(w, "permission denied or invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if r.FormValue("acknowledge_delete") != "yes" {
		s.renderLifecycleError(w, r, user, site, "Confirm that you understand this permanently deletes the site's files and managed WordPress database.")
		return
	}
	if _, err := s.store.EnqueueSiteDelete(r.Context(), user, site.ID, r.FormValue("confirmation")); err != nil {
		s.renderLifecycleError(w, r, user, site, err.Error())
		return
	}
	http.Redirect(w, r, "/jobs?delete=queued", http.StatusSeeOther)
}

func (s *Server) renderLifecycleError(w http.ResponseWriter, r *http.Request, user store.User, site model.Site, message string) {
	data := pageData{Title: "Site settings", Section: "settings", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, Error: message,
		Form: map[string]string{"domain": r.FormValue("domain"), "confirmation": r.FormValue("confirmation"), "acknowledge_delete": r.FormValue("acknowledge_delete")}}
	if snippets, err := s.store.SiteSnippets(r.Context(), site.ID); err == nil {
		data.SiteSnippets = &snippets
	}
	s.renderStatus(w, "site.html", http.StatusBadRequest, data)
}
