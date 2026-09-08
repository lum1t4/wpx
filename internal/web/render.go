package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/cloudmeta"
	"github.com/lum1t4/wpx/internal/localize"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

// Rendering buffers the complete template before writing headers. A template
// error must produce a failure response, not a truncated page with status 200.

type pageData struct {
	Cloud                              *cloudmeta.Result
	Locale                             localize.Locale
	Languages                          []localize.Option
	CurrentPath                        string
	NodeRuntime                        *model.NodeRuntime
	FTPUsers                           []model.FTPUser
	MailService                        *model.MailService
	Title                              string
	Section                            string
	NavSection                         string
	Query                              string
	KindFilter                         string
	SiteCount                          int
	ActiveSiteCount                    int
	AttentionSiteCount                 int
	NewSite                            bool
	Form                               map[string]string
	SelectedSiteIDs                    []string
	User                               *store.User
	CSRF                               string
	Error                              string
	Message                            string
	Sites                              []model.Site
	SetupOpen                          bool
	TOTPSecret                         string
	TOTPUri                            string
	RecoveryCodes                      []string
	Users                              []store.User
	CanManageSites                     bool
	CanManageUsers                     bool
	Site                               *model.Site
	CanWordPressLogin                  bool
	CanManageTLS                       bool
	CanManageWordPress                 bool
	Plugins                            []broker.WordPressPlugin
	Themes                             []broker.WordPressTheme
	CoreVersion                        string
	CoreUpdateVersion                  string
	CanManageFiles                     bool
	Files                              []broker.FileEntry
	FilePath                           string
	DirectoryPath                      string
	FileContent                        string
	ParentPath                         string
	EditingFile                        bool
	CanManageServer                    bool
	CanManageBackups                   bool
	CanManageDatabases                 bool
	CanDeploySite                      bool
	CanViewLogs                        bool
	StagingSites                       []model.Site
	ProductionSite                     *model.Site
	StagingUsername                    string
	StagingPassword                    string
	Observability                      *broker.SiteObservabilityResult
	RequestLogs                        *RequestLogView
	CanManageDNS                       bool
	DNSProviders                       []model.DNSProvider
	DNSRecords                         []model.DNSRecord
	BackupTargets                      []model.BackupTarget
	BackupSnapshots                    []model.BackupSnapshot
	BackupSchedules                    []model.BackupSchedule
	BackupRuns                         []model.BackupRun
	BackupPassword                     string
	GoogleCallbackURI                  string
	StagingInspection                  *broker.StagingInspection
	WordPressHealth                    *broker.WordPressHealthResult
	UpdateStatus                       *store.UpdateStatus
	UpdateAvailable                    bool
	SiteSnippets                       *model.SiteSnippets
	Jobs                               []store.JobSummary
	ActiveJobs                         bool
	SelectedUser                       *store.User
	Monitoring                         *monitoringView
	Databases                          []model.Database
	SelectedDatabase                   *model.Database
	DatabaseAdminStatus                string
	FleetSites                         []fleetSiteView
	FleetSummary                       fleetSummaryView
	FleetOutcomes                      []fleetUpdateOutcome
	CronSchedules                      []model.CronSchedule
	WordPressCronSetting               *model.WordPressCronSetting
	WordPressDebugStatus               *model.WordPressDebugStatus
	WordPressDebugLog                  *model.WordPressDebugLog
	WordPressSearchReplace             *broker.WordPressSearchReplaceResult
	WordPressSearchReplacePreviewToken string
	CanManageCron                      bool
	SiteAccess                         *model.SiteAccessSettings
	SecuritySettings                   *model.SecuritySettings
	SecurityReport                     *broker.SecurityReport
	SecurityAvailable                  bool
	SecurityInstallStatus              string
	OperatorAlerts                     *operatorAlertsView
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	s.renderStatus(w, name, http.StatusOK, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, name string, status int, data pageData) {
	data.Locale = localize.English
	data.Languages = localize.Supported()
	data.CurrentPath = "/"
	if request, ok := w.(localize.RequestWriter); ok {
		data.Locale = request.Locale()
		data.CurrentPath = request.CurrentPath()
	}
	preparePageData(name, &data)
	if data.CanManageServer && (name == "hosting.html" || name == "dashboard.html") && s.cloud != nil {
		if result, ready := s.cloud.Result(); ready && result.PublicIP != "" {
			data.Cloud = &result
		}
	}
	// A broken template must not become a truncated 200 response. Render into
	// memory before committing headers; no user data is cached between requests.
	var body bytes.Buffer
	if err := s.templates.ExecuteTemplate(&body, name, data); err != nil {
		s.logger.Error("render template", "template", name, "error", err)
		http.Error(w, "could not render this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(localize.TranslateHTML(body.Bytes(), data.Locale))
}

func humanBytes(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	number := float64(value)
	unit := 0
	for number >= 1024 && unit < len(units)-1 {
		number /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", number, units[unit])
}

func jobLabel(kind string) string {
	labels := map[string]string{
		"site.cron_apply":             "Update scheduled task",
		"site.wordpress_cron_apply":   "Update WordPress cron",
		"wordpress.security_install":  "Install WordPress defenses",
		"wordpress.security_apply":    "Apply WordPress defenses",
		"hosting.node_apply":          "Configure Node runtime",
		"hosting.ftp_apply":           "Create FTP account",
		"hosting.ftp_delete":          "Delete FTP account",
		"hosting.mail_apply":          "Configure outbound mail",
		"site.access_apply":           "Update site access",
		"site.provision":              "Create site",
		"site.disable":                "Disable site",
		"site.enable":                 "Enable site",
		"site.php_version":            "Change PHP version",
		"site.domain_change":          "Change website domain",
		"site.delete":                 "Delete site",
		"site.config_apply":           "Apply expert configuration",
		"site.certificate":            "Issue certificate",
		"site.certificate_dns":        "Issue DNS certificate",
		"site.backup":                 "Create backup",
		"site.restore":                "Restore backup",
		"site.restore_clone":          "Restore as new site",
		"database.create":             "Create database",
		"database.delete":             "Delete database",
		"database.admin_install":      "Install phpMyAdmin",
		"site.restore_test":           "Test backup restore",
		"wordpress.update":            "Update WordPress",
		"wordpress.search_replace":    "WordPress search and replace",
		"wordpress.performance_apply": "Apply WordPress performance settings",
		"wordpress.staging_create":    "Create staging",
		"wordpress.staging_sync":      "Sync staging",
		"wordpress.staging_deploy":    "Deploy staging",
		"dns.provider_verify":         "Verify DNS provider",
		"dns.record_apply":            "Apply DNS record",
		"dns.record_delete":           "Delete DNS record",
	}
	if label := labels[kind]; label != "" {
		return label
	}
	return strings.ReplaceAll(kind, ".", " · ")
}
