package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// User administration preserves the single-owner invariant in the store.
// Validation pages may retain non-secret form values, never submitted passwords.

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request, user store.User) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not load users", http.StatusInternalServerError)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	s.render(w, "users.html", pageData{Title: "Users", User: &user, CSRF: s.ensureCSRF(w, r), Users: users, Sites: sites, CanManageUsers: true})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderUsersError(w, r, actor, "The passwords do not match.")
		return
	}
	_, err := s.store.CreateUser(r.Context(), actor, r.FormValue("username"), password, rbac.Role(r.FormValue("role")), r.Form["site_ids"])
	if err != nil {
		s.renderUsersError(w, r, actor, err.Error())
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) editUserPage(w http.ResponseWriter, r *http.Request, actor store.User) {
	target, err := s.store.User(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if target.Role == rbac.Owner || target.ID == actor.ID || (actor.Role == rbac.Administrator && target.Role == rbac.Administrator) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	s.render(w, "user_edit.html", pageData{Title: "Edit " + target.Username, User: &actor, CSRF: s.ensureCSRF(w, r), Sites: sites, SelectedUser: &target, CanManageUsers: true})
}

func (s *Server) updateUserAccess(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if err := s.store.UpdateUserAccess(r.Context(), actor, r.PathValue("id"), rbac.Role(r.FormValue("role")), r.Form["site_ids"]); err != nil {
		target, loadErr := s.store.User(r.Context(), r.PathValue("id"))
		if loadErr != nil {
			http.NotFound(w, r)
			return
		}
		sites, loadErr := s.store.ListSites(r.Context())
		if loadErr != nil {
			http.Error(w, "could not load sites", http.StatusInternalServerError)
			return
		}
		// Preserve the attempted edit, not the unchanged database values. Only
		// presentation state is replaced; a failed store operation grants nothing.
		target.Role = rbac.Role(r.FormValue("role"))
		target.SiteIDs = append([]string(nil), r.Form["site_ids"]...)
		s.renderStatus(w, "user_edit.html", http.StatusBadRequest, pageData{Title: "Edit user", User: &actor, CSRF: s.ensureCSRF(w, r), Sites: sites, SelectedUser: &target, CanManageUsers: true, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) setUserStatus(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	action := r.FormValue("action")
	if action != "enable" && action != "disable" {
		s.renderUsersError(w, r, actor, "Choose enable or disable.")
		return
	}
	if err := s.store.SetUserDisabled(r.Context(), actor, r.PathValue("id"), action == "disable"); err != nil {
		s.renderUsersError(w, r, actor, err.Error())
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) renderUsersError(w http.ResponseWriter, r *http.Request, actor store.User, message string) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not load users", http.StatusInternalServerError)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	// This allowlist intentionally excludes both password fields. Retaining a
	// failed creation should never reflect credentials back into the document.
	form := map[string]string{"username": r.FormValue("username"), "role": r.FormValue("role")}
	s.renderStatus(w, "users.html", http.StatusBadRequest, pageData{
		Title: "Users", User: &actor, CSRF: s.ensureCSRF(w, r), Users: users,
		Sites: sites, CanManageUsers: true, Error: message, Form: form,
		SelectedSiteIDs: append([]string(nil), r.Form["site_ids"]...),
	})
}

func userAssignedSite(user store.User, siteID string) bool {
	return selectedSite(user.SiteIDs, siteID)
}

func selectedSite(siteIDs []string, siteID string) bool {
	for _, assigned := range siteIDs {
		if assigned == siteID {
			return true
		}
	}
	return false
}
