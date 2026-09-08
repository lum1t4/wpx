package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func (s *Server) registerWordPressDebugRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/wordpress/debug", s.requireSession(s.wordpressDebugPage))
	mux.HandleFunc("POST /sites/{id}/wordpress/debug", s.requireSession(s.setWordPressDebug))
	mux.HandleFunc("POST /sites/{id}/wordpress/debug/clear", s.requireSession(s.clearWordPressDebugLog))
}

func (s *Server) wordpressDebugPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedWordPressDebugSite(w, r, user)
	if !ok {
		return
	}
	if s.broker == nil {
		http.Error(w, "WordPress debug settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	var status model.WordPressDebugStatus
	request := broker.WordPressDebugRequest{Site: site}
	if err := s.broker.Call(r.Context(), broker.OpWordPressDebugStatus, "wordpress.debug.status:"+site.ID+":"+mustRandomHex(8), request, &status); err != nil {
		http.Error(w, "could not inspect WordPress debug settings", http.StatusBadGateway)
		return
	}
	var log model.WordPressDebugLog
	if status.LogExists {
		if err := s.broker.Call(r.Context(), broker.OpWordPressDebugRead, "wordpress.debug.read:"+site.ID+":"+mustRandomHex(8), request, &log); err != nil {
			http.Error(w, "could not read the WordPress debug log", http.StatusBadGateway)
			return
		}
	}
	data := pageData{Title: "WordPress debug · " + site.Domain, Section: "wordpress_debug", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageWordPress: true, WordPressDebugStatus: &status, WordPressDebugLog: &log}
	if r.URL.Query().Get("saved") == "enabled" {
		data.Message = "WordPress debug mode enabled."
	} else if r.URL.Query().Get("saved") == "disabled" {
		data.Message = "WordPress debug mode disabled."
	} else if r.URL.Query().Get("cleared") == "yes" {
		data.Message = "WordPress debug log cleared."
	}
	s.render(w, "wordpress_debug.html", data)
}

func (s *Server) setWordPressDebug(w http.ResponseWriter, r *http.Request, user store.User) {
	if !parseWordPressDebugForm(w, r) {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedWordPressDebugSite(w, r, user)
	if !ok {
		return
	}
	value := r.FormValue("enabled")
	if value != "true" && value != "false" {
		http.Error(w, "invalid WordPress debug setting", http.StatusBadRequest)
		return
	}
	enabled := value == "true"
	action := "wordpress.debug_disabled"
	if enabled {
		action = "wordpress.debug_enabled"
	}
	succeeded := s.broker != nil && s.broker.Call(r.Context(), broker.OpWordPressDebugSet, "wordpress.debug.set:"+site.ID+":"+mustRandomHex(12), broker.WordPressDebugSetRequest{Site: site, Enabled: &enabled}, nil) == nil
	if err := s.store.RecordWordPressDebugEvent(r.Context(), user, site.ID, action, succeeded); err != nil {
		s.logger.Error("record WordPress debug action", "site_id", site.ID, "error", err)
	}
	if !succeeded {
		http.Error(w, "could not update WordPress debug settings", http.StatusBadGateway)
		return
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/wordpress/debug?saved="+state, http.StatusSeeOther)
}

func (s *Server) clearWordPressDebugLog(w http.ResponseWriter, r *http.Request, user store.User) {
	if !parseWordPressDebugForm(w, r) {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedWordPressDebugSite(w, r, user)
	if !ok {
		return
	}
	succeeded := s.broker != nil && s.broker.Call(r.Context(), broker.OpWordPressDebugClear, "wordpress.debug.clear:"+site.ID+":"+mustRandomHex(12), broker.WordPressDebugRequest{Site: site}, nil) == nil
	if err := s.store.RecordWordPressDebugEvent(r.Context(), user, site.ID, "wordpress.debug_log_cleared", succeeded); err != nil {
		s.logger.Error("record WordPress debug log clear", "site_id", site.ID, "error", err)
	}
	if !succeeded {
		http.Error(w, "could not clear the WordPress debug log", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/wordpress/debug?cleared=yes", http.StatusSeeOther)
}

func parseWordPressDebugForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid WordPress debug request", http.StatusRequestEntityTooLarge)
		return false
	}
	return true
}

func (s *Server) authorizedWordPressDebugSite(w http.ResponseWriter, r *http.Request, user store.User) (model.Site, bool) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageWordPress)
	if !ok {
		return model.Site{}, false
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewLogs) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return model.Site{}, false
	}
	if err := model.ValidateWordPressDebugSite(site); err != nil {
		http.Error(w, "WordPress debug requires a WordPress site", http.StatusBadRequest)
		return model.Site{}, false
	}
	return site, true
}
