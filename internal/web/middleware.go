package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Middleware establishes sessions and cross-request protections. Site access is
// checked separately through authorizedSite; UI visibility is not authorization.

type userHandler func(http.ResponseWriter, *http.Request, store.User)

func (s *Server) requireSession(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("wpx_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		user, err := s.store.ResolveSession(r.Context(), cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, user)
	}
}

func (s *Server) requireCapability(capability rbac.Capability, next userHandler) userHandler {
	return func(w http.ResponseWriter, r *http.Request, user store.User) {
		if !rbac.Allows(user.Role, capability) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		next(w, r, user)
	}
}

func (s *Server) authorizedSite(w http.ResponseWriter, r *http.Request, user store.User, capability rbac.Capability) (model.Site, bool) {
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return model.Site{}, false
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, capability) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return model.Site{}, false
	}
	if !allowSiteMutation(w, r, site) {
		return model.Site{}, false
	}
	return site, true
}

// Reserved domain/deletion work may span a database export, a service reload,
// or irreversible cleanup. Keep short broker-backed edits out of that window
// too: those edits do not pass through the durable job queue's reservation.
// Reading Settings and Activity remains possible, as do explicit failed-job
// recovery requests. The store still rechecks state in its own transaction.
func allowSiteMutation(w http.ResponseWriter, r *http.Request, site model.Site) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	switch site.Status {
	case "domain_changing", "deleting":
	case "domain_change_failed":
		if r.URL.Path == "/sites/"+site.ID+"/domain/retry" {
			return true
		}
	case "delete_failed":
		if r.URL.Path == "/sites/"+site.ID+"/delete" {
			return true
		}
	default:
		return true
	}
	http.Error(w, "This site has an unfinished domain change or deletion. Check Activity and complete its recovery before making other changes.", http.StatusConflict)
	return false
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie("wpx_csrf"); err == nil && len(cookie.Value) == 64 {
		return cookie.Value
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic random source unavailable")
	}
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "wpx_csrf", Value: token, Path: "/", MaxAge: 86400, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return token
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("wpx_csrf")
	if err != nil || len(cookie.Value) != 64 {
		return false
	}
	provided := r.FormValue("csrf_token")
	if provided == "" {
		provided = r.Header.Get("X-CSRF-Token")
	}
	return len(provided) == len(cookie.Value) && subtle.ConstantTimeCompare([]byte(provided), []byte(cookie.Value)) == 1
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: token, Path: "/", MaxAge: 1800, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func mustRandomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic random source unavailable")
	}
	return hex.EncodeToString(b)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("web panic", "value", recovered)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
