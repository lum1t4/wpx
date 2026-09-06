package web

import (
	"io/fs"
	"net/http"

	"github.com/lum1t4/wpx/internal/rbac"
)

// Routes keep the HTTP surface in one place. Server-wide operations declare
// their role requirement here; site operations check assignment and capability
// in the handler because a role alone does not grant access to every site.

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(templateFiles, "static")
	if err != nil {
		panic("embedded static filesystem is invalid: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("GET /login/totp", s.loginTOTPPage)
	mux.HandleFunc("POST /login/totp", s.loginTOTPSubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.requireSession(s.dashboard))
	mux.HandleFunc("GET /jobs", s.requireSession(s.jobsPage))
	mux.HandleFunc("GET /sites", s.requireSession(s.sitesPage))
	mux.HandleFunc("GET /sites/new", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.newSitePage)))
	mux.HandleFunc("POST /sites", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.createSite)))
	mux.HandleFunc("GET /sites/{id}", s.requireSession(s.sitePage))
	for _, section := range []string{"wordpress", "staging", "backups", "security", "settings"} {
		mux.HandleFunc("GET /sites/{id}/"+section, s.requireSession(s.sitePage))
	}
	mux.HandleFunc("POST /sites/{id}/disable", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.disableSite)))
	mux.HandleFunc("POST /sites/{id}/enable", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.enableSite)))
	mux.HandleFunc("POST /sites/{id}/retry", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.retrySiteProvision)))
	mux.HandleFunc("POST /sites/{id}/expert-config", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.saveSiteSnippets)))
	mux.HandleFunc("POST /sites/{id}/php-version", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.changePHPVersion)))
	mux.HandleFunc("POST /sites/{id}/wordpress/login", s.requireSession(s.wordpressLogin))
	mux.HandleFunc("POST /sites/{id}/wordpress/performance", s.requireSession(s.setWordPressPerformance))
	mux.HandleFunc("POST /sites/{id}/wordpress/update", s.requireSession(s.updateWordPress))
	mux.HandleFunc("GET /sites/{id}/wordpress/health", s.requireSession(s.wordpressHealthPage))
	mux.HandleFunc("POST /sites/{id}/certificate", s.requireSession(s.issueCertificate))
	mux.HandleFunc("POST /sites/{id}/backups", s.requireSession(s.createSiteBackup))
	mux.HandleFunc("POST /sites/{id}/backups/schedule", s.requireSession(s.setSiteBackupSchedule))
	mux.HandleFunc("POST /sites/{id}/backups/{snapshot}/restore", s.requireSession(s.restoreSiteBackup))
	mux.HandleFunc("POST /sites/{id}/backups/{snapshot}/clone", s.requireSession(s.restoreSiteBackupClone))
	mux.HandleFunc("POST /sites/{id}/backups/restore-test", s.requireSession(s.testSiteBackupRestore))
	mux.HandleFunc("POST /sites/{id}/backups/restore-test-schedule", s.requireSession(s.setRestoreTestSchedule))
	mux.HandleFunc("POST /sites/{id}/staging", s.requireSession(s.createStaging))
	mux.HandleFunc("POST /sites/{id}/staging/sync", s.requireSession(s.syncStaging))
	mux.HandleFunc("GET /sites/{id}/staging/deploy", s.requireSession(s.stagingDeployPage))
	mux.HandleFunc("POST /sites/{id}/staging/deploy", s.requireSession(s.deployStaging))
	mux.HandleFunc("GET /sites/{id}/observability", s.requireSession(s.observabilityPage))
	mux.HandleFunc("GET /sites/{id}/dns", s.requireSession(s.siteDNSPage))
	mux.HandleFunc("POST /sites/{id}/dns", s.requireSession(s.createSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/dns/{record}", s.requireSession(s.updateSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/dns/{record}/delete", s.requireSession(s.deleteSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/wordpress/plugins/{plugin}", s.requireSession(s.setWordPressPlugin))
	mux.HandleFunc("GET /sites/{id}/files", s.requireSession(s.filesPage))
	mux.HandleFunc("POST /sites/{id}/files", s.requireSession(s.saveFile))
	mux.HandleFunc("GET /account/security", s.requireSession(s.securityPage))
	mux.HandleFunc("POST /account/totp/begin", s.requireSession(s.beginTOTP))
	mux.HandleFunc("POST /account/totp/confirm", s.requireSession(s.confirmTOTP))
	mux.HandleFunc("POST /account/totp/disable", s.requireSession(s.disableTOTP))
	mux.HandleFunc("POST /account/password", s.requireSession(s.changePassword))
	mux.HandleFunc("GET /users", s.requireSession(s.requireCapability(rbac.ManageUsers, s.usersPage)))
	mux.HandleFunc("POST /users", s.requireSession(s.requireCapability(rbac.ManageUsers, s.createUser)))
	mux.HandleFunc("GET /users/{id}", s.requireSession(s.requireCapability(rbac.ManageUsers, s.editUserPage)))
	mux.HandleFunc("POST /users/{id}", s.requireSession(s.requireCapability(rbac.ManageUsers, s.updateUserAccess)))
	mux.HandleFunc("POST /users/{id}/status", s.requireSession(s.requireCapability(rbac.ManageUsers, s.setUserStatus)))
	mux.HandleFunc("GET /backups/targets", s.requireSession(s.requireCapability(rbac.ManageServer, s.backupTargetsPage)))
	mux.HandleFunc("POST /backups/targets", s.requireSession(s.requireCapability(rbac.ManageServer, s.createBackupTarget)))
	mux.HandleFunc("GET /dns/providers", s.requireSession(s.requireCapability(rbac.ManageServer, s.dnsProvidersPage)))
	mux.HandleFunc("POST /dns/providers", s.requireSession(s.requireCapability(rbac.ManageServer, s.createDNSProvider)))
	return s.securityHeaders(s.recoverPanics(mux))
}
