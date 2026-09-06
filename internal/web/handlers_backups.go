package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Backup storage is server-owned, while backup operations are site-scoped.
// Mutations record durable jobs; a redirect means queued, not safely completed.

func (s *Server) backupTargetsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	targets, err := s.store.ListBackupTargets(r.Context())
	if err != nil {
		http.Error(w, "could not load backup targets", http.StatusInternalServerError)
		return
	}
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets})
}

func (s *Server) createBackupTarget(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	target := model.BackupTarget{
		Name: r.FormValue("name"), Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"),
		Prefix: r.FormValue("prefix"), Region: r.FormValue("region"), BucketLookup: r.FormValue("bucket_lookup"),
		AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key"), RepositoryPassword: r.FormValue("repository_password"),
		AllowInsecureHTTP: r.FormValue("allow_insecure_http") == "yes",
		DriveFolder:       r.FormValue("drive_folder"), GoogleClientID: r.FormValue("google_client_id"), GoogleClientSecret: r.FormValue("google_client_secret"), GoogleToken: r.FormValue("google_token"), GoogleSharedDrive: r.FormValue("google_shared_drive"),
	}
	var password string
	var err error
	if r.FormValue("kind") == string(model.BackupGoogleDrive) {
		_, password, err = s.store.CreateGoogleDriveTarget(r.Context(), user, target)
	} else {
		_, password, err = s.store.CreateS3Target(r.Context(), user, target)
	}
	if err != nil {
		targets, _ := s.store.ListBackupTargets(r.Context())
		s.renderStatus(w, "backup_targets.html", http.StatusBadRequest, pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, Error: err.Error()})
		return
	}
	if password == "" {
		http.Redirect(w, r, "/backups/targets", http.StatusSeeOther)
		return
	}
	targets, _ := s.store.ListBackupTargets(r.Context())
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, BackupPassword: password, Message: "Backup storage is being verified. Save the generated repository password now; WPX will not show it again."})
}

func (s *Server) createSiteBackup(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueSiteBackup(r.Context(), user, site.ID, r.FormValue("target_id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/backups?backup=queued", http.StatusSeeOther)
}

func (s *Server) setSiteBackupSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	interval, err := strconv.Atoi(r.FormValue("interval_hours"))
	if err != nil {
		http.Error(w, "invalid backup frequency", http.StatusBadRequest)
		return
	}
	keepDaily, dailyErr := strconv.Atoi(r.FormValue("keep_daily"))
	keepWeekly, weeklyErr := strconv.Atoi(r.FormValue("keep_weekly"))
	keepMonthly, monthlyErr := strconv.Atoi(r.FormValue("keep_monthly"))
	if dailyErr != nil || weeklyErr != nil || monthlyErr != nil {
		http.Error(w, "invalid backup retention", http.StatusBadRequest)
		return
	}
	retention := model.BackupRetention{KeepDaily: keepDaily, KeepWeekly: keepWeekly, KeepMonthly: keepMonthly}
	restoreTestDays := 0
	if value := r.FormValue("restore_test_days"); value != "" {
		restoreTestDays, err = strconv.Atoi(value)
		if err != nil {
			http.Error(w, "invalid restore test frequency", http.StatusBadRequest)
			return
		}
	}
	if err := s.store.SetBackupSchedule(r.Context(), user, site.ID, r.FormValue("target_id"), interval, retention, restoreTestDays); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/backups?saved=yes", http.StatusSeeOther)
}

func (s *Server) restoreSiteBackup(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the site domain exactly to confirm the restore", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EnqueueSiteRestore(r.Context(), user, site.ID, r.PathValue("snapshot")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/backups?restore=queued", http.StatusSeeOther)
}

func (s *Server) restoreSiteBackupClone(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	source, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if !s.store.UserCanSite(r.Context(), user, source.ID, rbac.DeploySite) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	destination := r.FormValue("destination")
	targetID, err := model.NewSiteID()
	if err != nil {
		s.logger.Error("generate restore destination identifier", "error", err)
		http.Error(w, "could not create restore destination", http.StatusInternalServerError)
		return
	}
	targetDomain := strings.ToLower(strings.TrimSpace(r.FormValue("target_domain")))
	_, password, err := s.store.EnqueueRestoreClone(r.Context(), user, source.ID, r.PathValue("snapshot"), targetID, targetDomain, destination)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if destination == "staging" {
		parentID := source.ID
		if source.Environment == "staging" && source.ParentSiteID != "" {
			parentID = source.ParentSiteID
		}
		target := model.Site{ID: targetID, Domain: targetDomain, Kind: model.WordPress, Status: "queued", Environment: "staging", ParentSiteID: parentID}
		s.render(w, "staging_created.html", pageData{Title: "Staging restore queued", User: &user, CSRF: s.ensureCSRF(w, r), Site: &target, StagingUsername: "wpx", StagingPassword: password, Message: "The encrypted snapshot is being restored into this protected staging environment."})
		return
	}
	http.Redirect(w, r, "/sites/"+targetID+"?clone=queued", http.StatusSeeOther)
}

func (s *Server) testSiteBackupRestore(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueRestoreTest(r.Context(), user, site.ID, r.FormValue("snapshot_id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/backups?restore-test=queued", http.StatusSeeOther)
}

func (s *Server) setRestoreTestSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	days, err := strconv.Atoi(r.FormValue("interval_days"))
	if err != nil {
		http.Error(w, "invalid restore test frequency", http.StatusBadRequest)
		return
	}
	if err := s.store.SetRestoreTestSchedule(r.Context(), user, site.ID, r.FormValue("target_id"), days); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/backups?saved=yes", http.StatusSeeOther)
}
