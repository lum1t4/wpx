package web

import (
	"net/http"
	"net/url"
	"regexp"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

var wordpressLoginFragment = regexp.MustCompile(`^[a-f0-9]{64}$`)

// WordPress actions authorize the selected site before crossing the broker
// boundary. Durable changes are queued; one-click login and inventory are reads
// or short operations whose result is needed for the current response.

func (s *Server) setWordPressPlugin(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if !allowSiteMutation(w, r, site) {
		return
	}
	action := r.FormValue("action")
	if action != "activate" && action != "deactivate" {
		http.Error(w, "invalid plugin action", http.StatusBadRequest)
		return
	}
	request := broker.WordPressPluginSetRequest{Site: site, Plugin: r.PathValue("plugin"), Active: action == "activate"}
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpWordPressPluginSet, "wordpress.plugin:"+site.ID+":"+mustRandomHex(12), request, nil) != nil {
		http.Error(w, "could not change the WordPress plugin", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/wordpress?saved=yes", http.StatusSeeOther)
}

func (s *Server) setWordPressPerformance(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	jobID, err := s.store.SetWordPressPerformance(r.Context(), user, site.ID, r.FormValue("redis") == "on", r.FormValue("fastcgi_cache") == "on")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.respondQueuedAction(w, r, user, jobID, "/sites/"+site.ID+"/wordpress?wordpress=queued", "Caching settings")
}

func (s *Server) updateWordPress(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	update := model.WordPressUpdate{Component: model.WordPressComponent(r.FormValue("component")), Name: r.FormValue("name")}
	if _, err := s.store.EnqueueWordPressUpdate(r.Context(), user, site.ID, r.FormValue("target_id"), update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/wordpress?update=queued", http.StatusSeeOther)
}

func (s *Server) wordpressHealthPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ViewSite)
	if !ok {
		return
	}
	if site.Kind != model.WordPress || site.Status != "active" {
		http.Error(w, "health checks require an active WordPress site", http.StatusBadRequest)
		return
	}
	if s.broker == nil {
		http.Error(w, "WordPress health checks are unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.WordPressHealthResult
	if err := s.broker.Call(r.Context(), broker.OpWordPressHealth, "wordpress.health:"+site.ID+":"+mustRandomHex(8), broker.WordPressPluginsRequest{Site: site}, &result); err != nil {
		http.Error(w, "could not run WordPress health checks", http.StatusBadGateway)
		return
	}
	s.render(w, "wordpress_health.html", pageData{Title: "Health · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, WordPressHealth: &result})
}

func (s *Server) wordpressLogin(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.WordPressLogin) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if !allowSiteMutation(w, r, site) {
		return
	}
	if s.broker == nil {
		http.Error(w, "WordPress operations are unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.WordPressLoginResult
	requestID := "wordpress.login:" + site.ID + ":" + mustRandomHex(12)
	if err := s.broker.Call(r.Context(), broker.OpWordPressLogin, requestID, broker.WordPressLoginRequest{Site: site}, &result); err != nil {
		s.renderStatus(w, "site.html", http.StatusBadGateway, pageData{Title: site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanWordPressLogin: true, Error: err.Error()})
		return
	}
	target, err := url.Parse(result.URL)
	expectedScheme := "http"
	if site.TLSStatus == "active" {
		expectedScheme = "https"
	}
	if err != nil || target.Scheme != expectedScheme || target.Host != site.Domain || target.User != nil || target.Path != "/wpx-login-handler.php" || target.RawQuery != "" || !wordpressLoginFragment.MatchString(target.Fragment) {
		http.Error(w, "could not open WordPress administration", http.StatusBadGateway)
		return
	}
	// http.Redirect writes a small HTML body containing the Location value.
	// This capability must exist only in the Location fragment, never a body.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", target.String())
	w.WriteHeader(http.StatusSeeOther)
}
