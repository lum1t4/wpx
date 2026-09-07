package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func (s *Server) registerSecurityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/security", s.requireSession(s.siteSecurityPage))
	mux.HandleFunc("POST /sites/{id}/security/install", s.requireSession(s.installSiteSecurity))
	mux.HandleFunc("POST /sites/{id}/security/settings", s.requireSession(s.setSiteSecurity))
	mux.HandleFunc("POST /sites/{id}/security/inspect", s.requireSession(s.inspectSiteSecurity))
}

func (s *Server) siteSecurityPage(w http.ResponseWriter, r *http.Request, user store.User) {
	data, ok := s.securityPageData(w, r, user)
	if !ok {
		return
	}
	s.render(w, "site.html", data)
}

func (s *Server) securityPageData(w http.ResponseWriter, r *http.Request, user store.User) (pageData, bool) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageTLS)
	if !ok {
		return pageData{}, false
	}
	data := pageData{
		Title: site.Domain, Section: "security", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site,
		CanManageTLS:       true,
		CanManageDNS:       s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageDNS),
		CanManageWordPress: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress),
		CanViewLogs:        s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewLogs),
	}
	providers, err := s.store.ListDNSProviders(r.Context())
	if err != nil {
		http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
		return pageData{}, false
	}
	data.DNSProviders = providers
	if site.Kind == model.WordPress {
		settings, err := s.store.SiteSecuritySettings(r.Context(), site.ID)
		if err != nil {
			http.Error(w, "could not load security settings", http.StatusInternalServerError)
			return pageData{}, false
		}
		data.SecuritySettings = &settings
		data.SecurityInstallStatus, err = s.store.SecurityInstallStatus(r.Context())
		if err != nil {
			http.Error(w, "could not load security installation status", http.StatusInternalServerError)
			return pageData{}, false
		}
		if s.broker != nil {
			var status broker.SecurityStatusResult
			if err := s.broker.Call(r.Context(), broker.OpSecurityStatus, "wordpress.security_status:"+site.ID+":"+mustRandomHex(8), struct{}{}, &status); err == nil {
				data.SecurityAvailable = status.Available
			}
		}
	}
	if r.URL.Query().Get("security") == "queued" {
		data.Message = "WordPress security settings are queued. Follow progress in Activity."
	}
	if r.URL.Query().Get("security") == "installing" {
		data.Message = "WordPress defenses are being installed. Follow progress in Activity."
	}
	return data, true
}

func (s *Server) installSiteSecurity(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageTLS)
	if !ok {
		return
	}
	if !rbac.Allows(user.Role, rbac.ManageServer) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if _, err := s.store.EnqueueSecurityInstall(r.Context(), user, site.ID); err != nil {
		data, loaded := s.securityPageData(w, r, user)
		if !loaded {
			return
		}
		data.Error = err.Error()
		s.renderStatus(w, "site.html", http.StatusBadRequest, data)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/security?security=installing", http.StatusSeeOther)
}

func (s *Server) setSiteSecurity(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageWordPress)
	if !ok {
		return
	}
	settings := model.SecuritySettings{
		Enabled:                 r.FormValue("enabled") == "yes",
		LoginProtection:         r.FormValue("login_protection") == "yes",
		XMLRPCProtection:        r.FormValue("xmlrpc_protection") == "yes",
		SensitivePathProtection: r.FormValue("sensitive_path_protection") == "yes",
		Burst404Protection:      r.FormValue("burst_404_protection") == "yes",
	}
	if err := model.ValidateSecuritySettings(site, settings); err != nil {
		s.renderSecurityError(w, r, user, settings, err)
		return
	}
	jobID, err := s.store.EnqueueSiteSecuritySettings(r.Context(), user, site, settings)
	if err != nil {
		s.renderSecurityError(w, r, user, settings, err)
		return
	}
	returnURL := "/sites/" + site.ID + "/security"
	if r.Header.Get("X-WPX-Action") != "partial" {
		returnURL += "?security=queued"
	}
	s.respondQueuedAction(w, r, user, jobID, returnURL, "WordPress defenses")
}

func (s *Server) inspectSiteSecurity(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageTLS)
	if !ok {
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewLogs) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	data, ok := s.securityPageData(w, r, user)
	if !ok {
		return
	}
	var report broker.SecurityReport
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpSecurityInspect, "wordpress.security_inspect:"+site.ID+":"+mustRandomHex(8), broker.SecurityInspectRequest{Site: site}, &report) != nil {
		data.Error = "Could not analyze the bounded site access log."
		s.renderStatus(w, "site.html", http.StatusBadGateway, data)
		return
	}
	data.SecurityReport = &report
	s.render(w, "site.html", data)
}

func (s *Server) renderSecurityError(w http.ResponseWriter, r *http.Request, user store.User, settings model.SecuritySettings, err error) {
	data, ok := s.securityPageData(w, r, user)
	if !ok {
		return
	}
	data.SecuritySettings = &settings
	data.Error = err.Error()
	s.renderStatus(w, "site.html", http.StatusBadRequest, data)
}
