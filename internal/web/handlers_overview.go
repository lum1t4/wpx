package web

import (
	"net/http"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
	"github.com/lum1t4/wpx/internal/updatecheck"
)

// Overview and activity use local state. Site logs are an explicit broker read,
// scoped to the selected site and the user's log-viewing capability.

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, user store.User) {
	sites, err := s.store.ListSitesForUser(r.Context(), user)
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Overview", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageServer: rbac.Allows(user.Role, rbac.ManageServer)}
	if data.CanManageServer {
		status, statusErr := s.store.UpdateStatus(r.Context())
		if statusErr != nil {
			http.Error(w, "could not load update status", http.StatusInternalServerError)
			return
		}
		if status.CheckedAt != "" {
			data.UpdateStatus = &status
			data.UpdateAvailable = updatecheck.IsNewer(status.CurrentVersion, status.LatestVersion)
		}
	}
	s.render(w, "dashboard.html", data)
}

func (s *Server) jobsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	jobs, err := s.store.RecentJobsForUser(r.Context(), user, 100)
	if err != nil {
		http.Error(w, "could not load activity", http.StatusInternalServerError)
		return
	}
	active := false
	for _, job := range jobs {
		if job.Status == "queued" || job.Status == "running" {
			active = true
			break
		}
	}
	data := pageData{Title: "Activity", User: &user, CSRF: s.ensureCSRF(w, r), Jobs: jobs, ActiveJobs: active, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageServer: rbac.Allows(user.Role, rbac.ManageServer)}
	if r.URL.Query().Get("delete") == "queued" {
		data.Message = "Site deletion queued. Follow cleanup here; the site is removed from Sites only after it completes."
	}
	s.render(w, "jobs.html", data)
}

func (s *Server) observabilityPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ViewLogs)
	if !ok {
		return
	}
	requestLogs, err := requestLogView(r, nil)
	if err != nil {
		empty := broker.SiteObservabilityResult{}
		s.renderStatus(w, "observability.html", http.StatusBadRequest, pageData{Title: "Logs · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanViewLogs: true, Observability: &empty, RequestLogs: &requestLogs})
		return
	}
	if s.broker == nil {
		http.Error(w, "site observability is unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.SiteObservabilityResult
	if err := s.broker.Call(r.Context(), broker.OpSiteObservability, "site.observability:"+site.ID+":"+mustRandomHex(8), broker.SiteObservabilityRequest{Site: site}, &result); err != nil {
		http.Error(w, "could not load local site data", http.StatusBadGateway)
		return
	}
	requestLogs, err = requestLogView(r, result.AccessLog)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("download") == "csv" {
		if err := writeRequestLogsCSV(w, requestLogs.Rows); err != nil {
			http.Error(w, "could not create request log export", http.StatusInternalServerError)
		}
		return
	}
	s.render(w, "observability.html", pageData{Title: "Logs · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanViewLogs: true, Observability: &result, RequestLogs: &requestLogs})
}
