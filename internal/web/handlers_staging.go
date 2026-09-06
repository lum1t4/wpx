package web

import (
	"net/http"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Staging keeps production and staging authorization separate. Deployment must
// authorize both environments and validate explicit destructive confirmation.

func (s *Server) createStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	stagingID := strings.TrimSpace(r.FormValue("id"))
	stagingDomain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	_, password, err := s.store.CreateStaging(r.Context(), user, site.ID, stagingID, stagingDomain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	staging := model.Site{ID: stagingID, Domain: stagingDomain, Kind: model.WordPress, Environment: "staging", ParentSiteID: site.ID, Status: "queued"}
	s.render(w, "staging_created.html", pageData{Title: "Staging created", User: &user, CSRF: s.ensureCSRF(w, r), Site: &staging, StagingUsername: "wpx", StagingPassword: password})
}

func (s *Server) syncStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if site.Environment != "staging" || r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the staging domain exactly to confirm replacement", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EnqueueStagingSync(r.Context(), user, site.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/staging?sync=queued", http.StatusSeeOther)
}

func (s *Server) deployStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	staging, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if staging.Environment != "staging" {
		http.Error(w, "only staging can be deployed", http.StatusBadRequest)
		return
	}
	production, err := s.store.Site(r.Context(), staging.ParentSiteID)
	if err != nil || !s.store.UserCanSite(r.Context(), user, production.ID, rbac.DeploySite) {
		http.Error(w, "production site is unavailable", http.StatusForbidden)
		return
	}
	if r.FormValue("confirmation") != production.Domain {
		http.Error(w, "type the production domain exactly to confirm deployment", http.StatusBadRequest)
		return
	}
	selection := model.StagingSelection{Full: r.FormValue("mode") == "full"}
	if !selection.Full {
		selection.Files = append([]string(nil), r.Form["files"]...)
		selection.Tables = append([]string(nil), r.Form["tables"]...)
	}
	if _, err := s.store.EnqueueStagingDeploy(r.Context(), user, staging.ID, r.FormValue("target_id"), selection); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+production.ID+"/staging?deploy=queued", http.StatusSeeOther)
}

func (s *Server) stagingDeployPage(w http.ResponseWriter, r *http.Request, user store.User) {
	staging, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if staging.Environment != "staging" || staging.Status != "active" {
		http.Error(w, "only an active staging site can be deployed", http.StatusBadRequest)
		return
	}
	production, err := s.store.Site(r.Context(), staging.ParentSiteID)
	if err != nil || !s.store.UserCanSite(r.Context(), user, production.ID, rbac.DeploySite) {
		http.Error(w, "production site is unavailable", http.StatusForbidden)
		return
	}
	if s.broker == nil {
		http.Error(w, "staging inspection is unavailable", http.StatusServiceUnavailable)
		return
	}
	var inspection broker.StagingInspection
	request := broker.InspectStagingRequest{Staging: staging, Production: production}
	if err := s.broker.Call(r.Context(), broker.OpInspectStaging, "wordpress.staging_inspect:"+staging.ID+":"+mustRandomHex(8), request, &inspection); err != nil {
		http.Error(w, "could not inspect staging changes", http.StatusBadGateway)
		return
	}
	targets, err := s.store.ListBackupTargets(r.Context())
	if err != nil {
		http.Error(w, "could not load recovery storage", http.StatusInternalServerError)
		return
	}
	s.render(w, "staging_deploy.html", pageData{Title: "Deploy " + staging.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &staging, ProductionSite: &production, BackupTargets: targets, CanDeploySite: true, StagingInspection: &inspection})
}
