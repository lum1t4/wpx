package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Site pages load only the data needed by the selected tool. Slow WordPress
// inventory belongs to its tool page, never to the lightweight site overview.

func (s *Server) sitesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	sites, err := s.store.ListSitesForUser(r.Context(), user)
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if kind != "" && !model.ValidSiteKind(model.SiteKind(kind)) {
		http.Error(w, "unknown site type", http.StatusBadRequest)
		return
	}
	filtered := make([]model.Site, 0, len(sites))
	for _, site := range sites {
		if kind != "" && string(site.Kind) != kind {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(site.Domain+" "+site.ID), strings.ToLower(query)) {
			continue
		}
		filtered = append(filtered, site)
	}
	s.render(w, "sites.html", pageData{Title: "Sites", User: &user, CSRF: s.ensureCSRF(w, r), Sites: filtered, Query: query, KindFilter: kind})
}

func (s *Server) newSitePage(w http.ResponseWriter, r *http.Request, user store.User) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = string(model.WordPress)
	}
	if !model.ValidSiteKind(model.SiteKind(kind)) {
		http.Error(w, "unknown site type", http.StatusBadRequest)
		return
	}
	s.render(w, "sites.html", pageData{Title: "Create site", User: &user, CSRF: s.ensureCSRF(w, r), NewSite: true, KindFilter: kind, Form: map[string]string{"kind": kind, "php_version": "8.4"}})
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	// The domain is the user-facing name. Generate the stable filesystem and
	// database identity here, never from a submitted field or a mutable domain.
	siteID, err := model.NewSiteID()
	if err != nil {
		s.logger.Error("generate site identifier", "error", err)
		http.Error(w, "could not create site", http.StatusInternalServerError)
		return
	}
	site := model.Site{
		ID: siteID, Domain: strings.ToLower(strings.TrimSpace(r.FormValue("domain"))),
		Kind: model.SiteKind(r.FormValue("kind")), PHPVersion: r.FormValue("php_version"), Upstream: strings.TrimSpace(r.FormValue("upstream")), AllowEOL: r.FormValue("allow_eol") == "yes",
		WordPressMultisite: model.WordPressMultisiteMode(r.FormValue("wordpress_multisite")),
	}
	if site.Kind != model.WordPress && site.Kind != model.PHP {
		site.PHPVersion = ""
	}
	if site.Kind != model.WordPress {
		site.WordPressMultisite = model.MultisiteDisabled
	}
	if err := model.ValidateSite(site); err != nil {
		s.renderSiteError(w, r, user, err.Error())
		return
	}
	if _, err := s.store.CreateSite(r.Context(), user, site); err != nil {
		s.renderSiteError(w, r, user, err.Error())
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?provision=queued", http.StatusSeeOther)
}

func (s *Server) sitePage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewSite) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	section := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/sites/"+site.ID), "/")
	if section == "" {
		section = "overview"
	}
	if !canViewSiteSection(user, site, section) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	data := pageData{Title: site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanWordPressLogin: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.WordPressLogin), CanManageTLS: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageTLS), CanManageWordPress: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress), CanManageFiles: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageFiles), CanManageBackups: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageBackups), CanManageDatabases: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageDatabases), CanDeploySite: s.store.UserCanSite(r.Context(), user, site.ID, rbac.DeploySite), CanViewLogs: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewLogs), CanManageDNS: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageDNS)}
	data.Section = section
	if section == "databases" {
		if !data.CanManageDatabases {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		data.Databases, err = s.store.ListDatabasesForSite(r.Context(), site.ID)
		if err != nil {
			http.Error(w, "could not load site databases", http.StatusInternalServerError)
			return
		}
		data.DatabaseAdminStatus, err = s.store.DatabaseAdminStatus(r.Context())
		if err != nil {
			http.Error(w, "could not load database tools", http.StatusInternalServerError)
			return
		}
	}
	if site.Kind == model.WordPress && (section == "overview" || section == "staging") {
		sites, listErr := s.store.ListSitesForUser(r.Context(), user)
		if listErr != nil {
			http.Error(w, "could not load site environments", http.StatusInternalServerError)
			return
		}
		if site.Environment == "production" {
			for _, candidate := range sites {
				if candidate.ParentSiteID == site.ID {
					data.StagingSites = append(data.StagingSites, candidate)
				}
			}
		} else if site.ParentSiteID != "" {
			for _, candidate := range sites {
				if candidate.ID == site.ParentSiteID {
					production := candidate
					data.ProductionSite = &production
					break
				}
			}
		}
	}
	if section == "security" && data.CanManageTLS {
		providers, listErr := s.store.ListDNSProviders(r.Context())
		if listErr != nil {
			http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
			return
		}
		data.DNSProviders = providers
	}
	if section == "settings" && data.CanManageSites {
		snippets, snippetErr := s.store.SiteSnippets(r.Context(), site.ID)
		if snippetErr != nil {
			http.Error(w, "could not load expert configuration", http.StatusInternalServerError)
			return
		}
		data.SiteSnippets = &snippets
	}
	if r.URL.Query().Get("backup") == "queued" {
		data.Message = "Backup queued. It will appear under restore points when complete."
	}
	if r.URL.Query().Get("restore") == "queued" {
		data.Message = "Restore queued. WPX will create a rollback snapshot before switching the live site."
	}
	if r.URL.Query().Get("clone") == "queued" {
		data.Message = "Snapshot restore queued. WPX is preparing this independent site without changing the source."
	}
	if r.URL.Query().Get("lifecycle") == "queued" {
		data.Message = "Site lifecycle change queued. WPX will reconcile traffic and runtimes in the background."
	}
	if r.URL.Query().Get("config") == "queued" {
		data.Message = "Expert configuration queued. WPX will syntax-test it before reloading services."
	}
	if r.URL.Query().Get("restore-test") == "queued" {
		data.Message = "Restore test queued. WPX will download the snapshot and validate it without touching the live site."
	}
	if r.URL.Query().Get("deploy") == "queued" {
		data.Message = "Deployment queued. WPX will create an encrypted production recovery point before switching files and the database."
	}
	if r.URL.Query().Get("update") == "queued" {
		data.Message = "WordPress update queued. WPX will create an encrypted recovery point and run health checks before activating it."
	}
	if r.URL.Query().Get("provision") == "queued" {
		data.Message = "Site creation queued. Follow its progress in Activity."
	}
	if r.URL.Query().Get("php") == "queued" {
		data.Message = "PHP version change queued. The site may be briefly unavailable while its runtime switches. Follow progress in Activity."
	}
	if r.URL.Query().Get("domain") == "queued" {
		data.Message = "Domain change queued. Follow progress in Activity, then issue a certificate for the new domain in SSL & security."
	}
	if r.URL.Query().Get("certificate") == "queued" {
		data.Message = "Certificate request queued. Follow its progress in Activity."
	}
	if r.URL.Query().Get("saved") == "yes" {
		data.Message = "Settings saved."
	}
	if r.URL.Query().Get("sync") == "queued" {
		data.Message = "Refresh from production queued."
	}
	if r.URL.Query().Get("wordpress") == "queued" {
		data.Message = "WordPress change queued. Follow its progress in Activity."
	}
	if r.URL.Query().Get("database") == "queued" {
		data.Message = "Database creation is queued. Activity will show when it is ready."
	}
	if r.URL.Query().Get("database-delete") == "queued" {
		data.Message = "Database deletion is queued."
	}
	if (section == "backups" && data.CanManageBackups) || (section == "wordpress" && data.CanManageWordPress) || (section == "staging" && data.CanDeploySite) {
		data.BackupTargets, err = s.store.ListBackupTargets(r.Context())
		if err != nil {
			http.Error(w, "could not load backup targets", http.StatusInternalServerError)
			return
		}
		if section == "backups" && data.CanManageBackups {
			data.BackupSnapshots, err = s.store.ListSiteSnapshots(r.Context(), site.ID)
			if err != nil {
				http.Error(w, "could not load backup snapshots", http.StatusInternalServerError)
				return
			}
			data.BackupSchedules, err = s.store.ListSiteBackupSchedules(r.Context(), site.ID)
			if err != nil {
				http.Error(w, "could not load backup schedules", http.StatusInternalServerError)
				return
			}
		}
	}
	if section == "wordpress" && data.CanManageWordPress && site.Status == "active" && s.broker != nil {
		var result broker.WordPressInventoryResult
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := s.broker.Call(ctx, broker.OpWordPressInventory, "wordpress.inventory:"+site.ID+":"+mustRandomHex(8), broker.WordPressPluginsRequest{Site: site}, &result); err != nil {
			s.logger.Warn("load WordPress inventory", "site", site.ID, "error", err)
			data.Error = "WordPress is active, but its core, plugin, and theme inventory could not be loaded."
		} else {
			data.Plugins = result.Plugins
			data.Themes = result.Themes
			data.CoreVersion = result.CoreVersion
			data.CoreUpdateVersion = result.CoreUpdateVersion
		}
	}
	s.render(w, "site.html", data)
}

func (s *Server) disableSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the site domain to confirm", http.StatusBadRequest)
		return
	}
	jobID, err := s.store.EnqueueSiteDisable(r.Context(), user, site.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.respondQueuedAction(w, r, user, jobID, "/sites/"+site.ID+"/settings?lifecycle=queued", "Disabling site")
}

func (s *Server) enableSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	jobID, err := s.store.EnqueueSiteEnable(r.Context(), user, site.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.respondQueuedAction(w, r, user, jobID, "/sites/"+site.ID+"/settings?lifecycle=queued", "Enabling site")
}

func (s *Server) retrySiteProvision(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.EnqueueSiteProvisionRetry(r.Context(), user, r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/sites/"+url.PathEscape(r.PathValue("id"))+"?provision=queued", http.StatusSeeOther)
}

func (s *Server) saveSiteSnippets(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	snippets := model.SiteSnippets{Nginx: r.FormValue("nginx"), PHP: r.FormValue("php")}
	jobID, err := s.store.SetSiteSnippets(r.Context(), user, site.ID, snippets)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.respondQueuedAction(w, r, user, jobID, "/sites/"+site.ID+"/settings?config=queued", "Configuration")
}

func (s *Server) issueCertificate(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageTLS) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	providerID := strings.TrimSpace(r.FormValue("provider_id"))
	var enqueueErr error
	if providerID == "" {
		_, enqueueErr = s.store.EnqueueCertificate(r.Context(), user, site.ID)
	} else {
		_, enqueueErr = s.store.EnqueueDNSCertificate(r.Context(), user, site.ID, providerID, r.FormValue("wildcard") == "yes")
	}
	if enqueueErr != nil {
		providers, _ := s.store.ListDNSProviders(r.Context())
		s.renderStatus(w, "site.html", http.StatusBadRequest, pageData{Title: site.Domain, Section: "security", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, DNSProviders: providers, Error: enqueueErr.Error()})
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/security?certificate=queued", http.StatusSeeOther)
}

func (s *Server) renderSiteError(w http.ResponseWriter, r *http.Request, user store.User, message string) {
	form := make(map[string]string)
	for _, key := range []string{"domain", "kind", "php_version", "upstream", "allow_eol", "wordpress_multisite"} {
		form[key] = r.FormValue(key)
	}
	s.renderStatus(w, "sites.html", http.StatusBadRequest, pageData{Title: "Create site", User: &user, CSRF: s.ensureCSRF(w, r), NewSite: true, KindFilter: form["kind"], Form: form, Error: message})
}
