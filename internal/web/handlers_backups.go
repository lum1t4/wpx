package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	callbackURI, _ := googleDriveCallbackURI(r)
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, GoogleCallbackURI: callbackURI})
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
	}
	if r.FormValue("kind") != string(model.BackupS3) {
		s.renderBackupTargetsError(w, r, user, errors.New("choose S3-compatible storage here; Google Drive uses its Connect button"))
		return
	}
	_, password, err := s.store.CreateS3Target(r.Context(), user, target)
	if err != nil {
		s.renderBackupTargetsError(w, r, user, err)
		return
	}
	if password == "" {
		http.Redirect(w, r, "/backups/targets", http.StatusSeeOther)
		return
	}
	targets, _ := s.store.ListBackupTargets(r.Context())
	callbackURI, _ := googleDriveCallbackURI(r)
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, BackupPassword: password, GoogleCallbackURI: callbackURI, Message: "Backup storage is being verified. Save the generated repository password now; WPX will not show it again."})
}

func (s *Server) startGoogleDriveOAuth(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	callbackURI, err := googleDriveCallbackURI(r)
	if err != nil {
		s.renderBackupTargetsError(w, r, user, err)
		return
	}
	target := model.BackupTarget{
		Kind: model.BackupGoogleDrive, Name: r.FormValue("name"),
		DriveFolder: r.FormValue("drive_folder"), GoogleClientID: r.FormValue("google_client_id"),
		GoogleClientSecret: r.FormValue("google_client_secret"), GoogleSharedDrive: r.FormValue("google_shared_drive"),
		RepositoryPassword: r.FormValue("repository_password"),
	}
	state, verifier, err := s.store.CreateGoogleDriveOAuthFlow(r.Context(), user, target, callbackURI)
	if err != nil {
		s.renderBackupTargetsError(w, r, user, err)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	parameters := url.Values{
		"client_id": {strings.TrimSpace(target.GoogleClientID)}, "redirect_uri": {callbackURI},
		"response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/drive.file"},
		"access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"},
		"state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, s.googleAuthURL+"?"+parameters.Encode(), http.StatusSeeOther)
}

func (s *Server) googleDriveOAuthCallback(w http.ResponseWriter, r *http.Request) {
	flow, err := s.store.ConsumeGoogleDriveOAuthFlow(r.Context(), r.URL.Query().Get("state"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !rbac.Allows(flow.Actor.Role, rbac.ManageServer) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		s.renderBackupTargetsError(w, r, flow.Actor, fmt.Errorf("Google authorization was not completed (%s)", safeOAuthError(providerError)))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.renderBackupTargetsError(w, r, flow.Actor, errors.New("Google did not return an authorization code"))
		return
	}
	token, err := s.exchangeGoogleDriveCode(r, flow, code)
	if err != nil {
		s.logger.Warn("Google Drive OAuth exchange failed", "error", err)
		s.renderBackupTargetsError(w, r, flow.Actor, errors.New("Google Drive could not be connected; verify the client credentials and exact redirect URI, then try again"))
		return
	}
	flow.Target.GoogleToken = token
	_, password, err := s.store.CreateGoogleDriveTarget(r.Context(), flow.Actor, flow.Target)
	if err != nil {
		s.renderBackupTargetsError(w, r, flow.Actor, err)
		return
	}
	targets, _ := s.store.ListBackupTargets(r.Context())
	callbackURI, _ := googleDriveCallbackURI(r)
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &flow.Actor, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, BackupPassword: password, GoogleCallbackURI: callbackURI, Message: "Google Drive is connected and the encrypted repository is being verified. Save the generated recovery password now."})
}

func (s *Server) exchangeGoogleDriveCode(r *http.Request, flow store.GoogleDriveOAuthFlow, code string) (string, error) {
	form := url.Values{
		"client_id": {flow.Target.GoogleClientID}, "client_secret": {flow.Target.GoogleClientSecret},
		"code": {code}, "code_verifier": {flow.Verifier}, "grant_type": {"authorization_code"},
		"redirect_uri": {flow.RedirectURI},
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.oauthHTTP.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", response.Status)
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" || result.RefreshToken == "" || !strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn <= 0 || !hasOAuthScope(result.Scope, "https://www.googleapis.com/auth/drive.file") {
		return "", errors.New("Google token response is incomplete")
	}
	rcloneToken := struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	}{result.AccessToken, "Bearer", result.RefreshToken, time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339Nano)}
	encoded, err := json.Marshal(rcloneToken)
	return string(encoded), err
}

func hasOAuthScope(granted, required string) bool {
	for _, scope := range strings.Fields(granted) {
		if scope == required {
			return true
		}
	}
	return false
}

func googleDriveCallbackURI(r *http.Request) (string, error) {
	host := strings.TrimSpace(r.Host)
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("open WPX using its public HTTPS domain before connecting Google Drive")
	}
	hostname := parsed.Hostname()
	if hostname == "" || net.ParseIP(hostname) != nil || strings.EqualFold(hostname, "localhost") || !strings.Contains(hostname, ".") {
		return "", errors.New("Google Drive requires WPX to be opened through a public HTTPS domain")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("the panel URL has an invalid port")
		}
	}
	return "https://" + parsed.Host + "/backups/google-drive/callback", nil
}

func safeOAuthError(value string) string {
	for _, allowed := range []string{"access_denied", "temporarily_unavailable", "server_error"} {
		if value == allowed {
			return strings.ReplaceAll(value, "_", " ")
		}
	}
	return "authorization error"
}

func (s *Server) renderBackupTargetsError(w http.ResponseWriter, r *http.Request, user store.User, err error) {
	targets, _ := s.store.ListBackupTargets(r.Context())
	callbackURI, _ := googleDriveCallbackURI(r)
	s.renderStatus(w, "backup_targets.html", http.StatusBadRequest, pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, GoogleCallbackURI: callbackURI, Error: err.Error()})
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
