package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Account changes act on the current user, not an arbitrary user ID. Pending
// TOTP enrollment must survive a mistyped code without enabling authentication.

func (s *Server) securityPage(w http.ResponseWriter, r *http.Request, user store.User) {
	message := ""
	if r.URL.Query().Get("username") == "saved" {
		message = "Username updated."
	}
	data := pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Message: message}
	s.pendingTOTP(r, user, &data)
	s.render(w, "security.html", data)
}

func (s *Server) registerUsernameRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /account/username", s.requireSession(s.changeUsername))
	mux.HandleFunc("POST /users/{id}/username", s.requireSession(s.requireCapability(rbac.ManageUsers, s.renameUser)))
}

func (s *Server) changeUsername(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	data := pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Form: map[string]string{"username": r.FormValue("username")}}
	if _, err := s.store.Authenticate(r.Context(), user.Username, r.FormValue("current_password")); err != nil {
		data.Error = "The current password is incorrect."
		s.pendingTOTP(r, user, &data)
		s.renderStatus(w, "security.html", http.StatusBadRequest, data)
		return
	}
	updated, err := s.store.ChangeUsername(r.Context(), user, user.ID, r.FormValue("username"))
	if err != nil {
		data.Error = err.Error()
		s.pendingTOTP(r, user, &data)
		s.renderStatus(w, "security.html", http.StatusBadRequest, data)
		return
	}
	user.Username = updated.Username
	http.Redirect(w, r, "/account/security?username=saved", http.StatusSeeOther)
}

func (s *Server) pendingTOTP(r *http.Request, user store.User, data *pageData) {
	enrollment, err := s.store.PendingTOTPEnrollment(r.Context(), user)
	if err != nil {
		s.logger.Warn("read pending TOTP enrollment", "user", user.ID, "error", err)
		return
	}
	data.TOTPSecret, data.TOTPUri = enrollment.Secret, enrollment.URI
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.Authenticate(r.Context(), user.Username, r.FormValue("current_password")); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The current password is incorrect."})
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The new passwords do not match."})
		return
	}
	if err := s.store.ChangePassword(r.Context(), user, password); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: err.Error()})
		return
	}
	s.clearLoginChallenge(w)
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	enrollment, err := s.store.BeginTOTPEnrollment(r.Context(), user)
	if err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Error: err.Error()})
		return
	}
	s.render(w, "security.html", pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), TOTPSecret: enrollment.Secret, TOTPUri: enrollment.URI})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	codes, err := s.store.ConfirmTOTPEnrollment(r.Context(), user.ID, r.FormValue("code"))
	if err != nil {
		data := pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: err.Error()}
		s.pendingTOTP(r, user, &data)
		s.renderStatus(w, "security.html", http.StatusBadRequest, data)
		return
	}
	user.TOTPEnabled = true
	s.render(w, "security.html", pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Message: "Two-factor authentication is enabled. Save these recovery codes now; they will not be shown again.", RecoveryCodes: codes})
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.Authenticate(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The password is incorrect."})
		return
	}
	if err := s.store.DisableTOTP(r.Context(), user.ID); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Error: err.Error()})
		return
	}
	user.TOTPEnabled = false
	s.render(w, "security.html", pageData{Title: "Account settings", User: &user, CSRF: s.ensureCSRF(w, r), Message: "Two-factor authentication is disabled and its recovery codes were invalidated."})
}
