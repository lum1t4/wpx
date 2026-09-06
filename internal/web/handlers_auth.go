package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/store"
)

// Authentication uses short-lived server-side sessions. Setup is available only
// before an owner exists; TOTP challenges are distinct from authenticated sessions.

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		return
	}
	if exists {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup.html", pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), SetupOpen: true})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil || exists {
		http.Error(w, "setup is closed", http.StatusForbidden)
		return
	}
	provided := sha256.Sum256([]byte(r.FormValue("bootstrap_token")))
	expected, err := hex.DecodeString(s.cfg.BootstrapTokenHash)
	if err != nil || len(expected) != sha256.Size || subtle.ConstantTimeCompare(provided[:], expected) != 1 {
		s.renderStatus(w, "setup.html", http.StatusUnauthorized, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: "The bootstrap token is invalid.", SetupOpen: true})
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderStatus(w, "setup.html", http.StatusBadRequest, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: "The passwords do not match.", SetupOpen: true})
		return
	}
	user, err := s.store.CreateOwner(r.Context(), strings.TrimSpace(r.FormValue("username")), password)
	if err != nil {
		s.renderStatus(w, "setup.html", http.StatusBadRequest, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: err.Error(), SetupOpen: true})
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	exists, _ := s.store.OwnerExists(r.Context())
	if !exists {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", pageData{Title: "Sign in", CSRF: s.ensureCSRF(w, r)})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	user, err := s.store.Authenticate(r.Context(), strings.TrimSpace(r.FormValue("username")), r.FormValue("password"))
	if err != nil {
		// A small fixed delay makes online guessing more expensive without exposing
		// whether bcrypt or the account lookup caused the rejection.
		time.Sleep(250 * time.Millisecond)
		s.renderStatus(w, "login.html", http.StatusUnauthorized, pageData{Title: "Sign in", CSRF: s.ensureCSRF(w, r), Error: "The username or password is incorrect."})
		return
	}
	if user.TOTPEnabled {
		challenge, err := s.store.CreateLoginChallenge(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "could not create login challenge", http.StatusInternalServerError)
			return
		}
		s.setLoginChallenge(w, challenge)
		http.Redirect(w, r, "/login/totp", http.StatusSeeOther)
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginTOTPPage(w http.ResponseWriter, r *http.Request) {
	challenge, user, err := s.loginChallenge(r)
	if err != nil {
		s.clearLoginChallenge(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = challenge
	s.render(w, "login_totp.html", pageData{Title: "Verify sign-in", User: &user, CSRF: s.ensureCSRF(w, r)})
}

func (s *Server) loginTOTPSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	challenge, user, err := s.loginChallenge(r)
	if err != nil {
		s.clearLoginChallenge(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := s.store.VerifySecondFactor(r.Context(), user.ID, r.FormValue("code")); err != nil {
		time.Sleep(250 * time.Millisecond)
		s.renderStatus(w, "login_totp.html", http.StatusUnauthorized, pageData{Title: "Verify sign-in", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The authenticator or recovery code is invalid."})
		return
	}
	if err := s.store.DeleteLoginChallenge(r.Context(), challenge); err != nil {
		http.Error(w, "could not complete login", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.clearLoginChallenge(w)
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginChallenge(r *http.Request) (string, store.User, error) {
	cookie, err := r.Cookie("wpx_login")
	if err != nil {
		return "", store.User{}, err
	}
	user, err := s.store.ResolveLoginChallenge(r.Context(), cookie.Value)
	return cookie.Value, user, err
}

func (s *Server) setLoginChallenge(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_login", Value: token, Path: "/login/totp", MaxAge: 300, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearLoginChallenge(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_login", Value: "", Path: "/login/totp", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie("wpx_session"); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
