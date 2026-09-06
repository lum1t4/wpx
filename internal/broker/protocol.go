// Package broker defines the entire root boundary. Adding a privileged feature
// starts here: a typed operation, strict validation, an idempotency story, and
// tests that prove paths and arguments cannot escape their trusted namespace.
package broker

import (
	"encoding/json"

	"github.com/lum1t4/wpx/internal/model"
)

const ProtocolVersion = 1

type Operation string

const (
	OpProbe                Operation = "system.probe"
	OpEnsureSiteRoot       Operation = "site.ensure_root"
	OpProvisionSite        Operation = "site.provision"
	OpDisableSite          Operation = "site.disable"
	OpDeleteSite           Operation = "site.delete"
	OpChangeDomain         Operation = "site.domain"
	OpChangePHPVersion     Operation = "site.php_version"
	OpApplySiteSnippets    Operation = "site.apply_snippets"
	OpWordPressLogin       Operation = "wordpress.magic_login"
	OpIssueCertificate     Operation = "site.issue_certificate"
	OpIssueDNSCertificate  Operation = "site.issue_dns_certificate"
	OpWordPressPlugins     Operation = "wordpress.plugins"
	OpWordPressPluginSet   Operation = "wordpress.plugin_set"
	OpWordPressInventory   Operation = "wordpress.inventory"
	OpWordPressHealth      Operation = "wordpress.health"
	OpFileList             Operation = "file.list"
	OpFileRead             Operation = "file.read"
	OpFileWrite            Operation = "file.write"
	OpBackupTargetInit     Operation = "backup.target_init"
	OpBackupSite           Operation = "site.backup"
	OpRestoreSite          Operation = "site.restore"
	OpRestoreClone         Operation = "site.restore_clone"
	OpTestRestore          Operation = "site.restore_test"
	OpCreateStaging        Operation = "wordpress.staging_create"
	OpInspectStaging       Operation = "wordpress.staging_inspect"
	OpDeployStaging        Operation = "wordpress.staging_deploy"
	OpSiteObservability    Operation = "site.observability"
	OpWordPressPerformance Operation = "wordpress.performance_apply"
	OpWordPressUpdate      Operation = "wordpress.update"
)

type Request struct {
	Version        int             `json:"version"`
	ID             string          `json:"id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Operation      Operation       `json:"operation"`
	Payload        json.RawMessage `json:"payload"`
}

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type EnsureSiteRootRequest struct {
	SiteID string `json:"site_id"`
}

type EnsureSiteRootResult struct {
	Created bool `json:"created"`
}

type ProvisionSiteRequest struct {
	Site model.Site `json:"site"`
}

type DisableSiteRequest struct {
	Site    model.Site `json:"site"`
	StopPHP bool       `json:"stop_php"`
}

type ChangePHPVersionRequest struct {
	Site   model.Site             `json:"site"`
	Change model.PHPVersionChange `json:"change"`
}

type ChangePHPVersionResult struct {
	PreviousRestored bool `json:"previous_restored"`
}

type ApplySiteSnippetsRequest struct {
	Site     model.Site         `json:"site"`
	Snippets model.SiteSnippets `json:"snippets"`
}

type WordPressLoginRequest struct {
	Site model.Site `json:"site"`
}

type WordPressLoginResult struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

type IssueCertificateRequest struct {
	Site model.Site `json:"site"`
}

type IssueDNSCertificateRequest struct {
	Site     model.Site        `json:"site"`
	Provider model.DNSProvider `json:"provider"`
	Wildcard bool              `json:"wildcard"`
}

type WordPressPluginsRequest struct {
	Site model.Site `json:"site"`
}

type WordPressPlugin struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	Update        string `json:"update"`
	UpdateVersion string `json:"update_version"`
}

type WordPressPluginsResult struct {
	Plugins []WordPressPlugin `json:"plugins"`
}

type WordPressTheme struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	Update        string `json:"update"`
	UpdateVersion string `json:"update_version"`
}

type WordPressInventoryResult struct {
	CoreVersion       string            `json:"core_version"`
	CoreUpdateVersion string            `json:"core_update_version,omitempty"`
	Plugins           []WordPressPlugin `json:"plugins"`
	Themes            []WordPressTheme  `json:"themes"`
}

type WordPressHealthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type WordPressHealthResult struct {
	Checks []WordPressHealthCheck `json:"checks"`
}

type WordPressPluginSetRequest struct {
	Site   model.Site `json:"site"`
	Plugin string     `json:"plugin"`
	Active bool       `json:"active"`
}

type FileRequest struct {
	Site model.Site `json:"site"`
	Path string     `json:"path"`
}

type FileWriteRequest struct {
	Site    model.Site `json:"site"`
	Path    string     `json:"path"`
	Content string     `json:"content"`
}

type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type FileListResult struct {
	Entries []FileEntry `json:"entries"`
}

type FileReadResult struct {
	Content string `json:"content"`
}

type BackupTargetInitRequest struct {
	Target model.BackupTarget `json:"target"`
}

type BackupSiteRequest struct {
	Site      model.Site            `json:"site"`
	Target    model.BackupTarget    `json:"target"`
	Retention model.BackupRetention `json:"retention,omitempty"`
	JobKey    string                `json:"job_key"`
}

type BackupSiteResult struct {
	SnapshotID          string   `json:"snapshot_id"`
	RetentionApplied    bool     `json:"retention_applied,omitempty"`
	RetainedSnapshotIDs []string `json:"retained_snapshot_ids,omitempty"`
}

type RestoreSiteRequest struct {
	Site       model.Site         `json:"site"`
	Target     model.BackupTarget `json:"target"`
	SnapshotID string             `json:"snapshot_id"`
	JobKey     string             `json:"job_key"`
}

type RestoreSiteResult struct {
	RollbackSnapshotID string `json:"rollback_snapshot_id"`
}

type RestoreCloneRequest struct {
	Source       model.Site         `json:"source"`
	Target       model.Site         `json:"target"`
	BackupTarget model.BackupTarget `json:"backup_target"`
	SnapshotID   string             `json:"snapshot_id"`
	Username     string             `json:"username,omitempty"`
	Password     string             `json:"password,omitempty"`
	JobKey       string             `json:"job_key"`
}

type TestRestoreRequest struct {
	Site       model.Site         `json:"site"`
	Target     model.BackupTarget `json:"target"`
	SnapshotID string             `json:"snapshot_id"`
	JobKey     string             `json:"job_key"`
}

type CreateStagingRequest struct {
	Source   model.Site `json:"source"`
	Target   model.Site `json:"target"`
	Username string     `json:"username"`
	Password string     `json:"password"`
}

type DeployStagingRequest struct {
	Staging      model.Site         `json:"staging"`
	Production   model.Site         `json:"production"`
	BackupTarget model.BackupTarget `json:"backup_target"`
	Selection    StagingSelection   `json:"selection"`
	JobKey       string             `json:"job_key"`
}

type InspectStagingRequest struct {
	Staging    model.Site `json:"staging"`
	Production model.Site `json:"production"`
}

type StagingFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
}

type StagingInspection struct {
	Files  []StagingFileChange `json:"files"`
	Tables []string            `json:"tables"`
}

// Full is mutually exclusive with Files and Tables. A custom deployment may
// include either side independently, which keeps a content-only database push
// or a code-only file push a deliberate operator choice.
type StagingSelection = model.StagingSelection

type DeployStagingResult struct {
	RecoverySnapshotID string `json:"recovery_snapshot_id"`
}

type SiteObservabilityRequest struct {
	Site model.Site `json:"site"`
}

type SiteObservabilityResult struct {
	DiskBytes     int64    `json:"disk_bytes"`
	FileCount     int64    `json:"file_count"`
	ScanTruncated bool     `json:"scan_truncated,omitempty"`
	AccessLog     []string `json:"access_log"`
	ErrorLog      []string `json:"error_log"`
}

type WordPressPerformanceRequest struct {
	Site model.Site `json:"site"`
}

type WordPressUpdateRequest struct {
	Site         model.Site            `json:"site"`
	BackupTarget model.BackupTarget    `json:"backup_target"`
	Update       model.WordPressUpdate `json:"update"`
	JobKey       string                `json:"job_key"`
}

type WordPressUpdateResult struct {
	RecoverySnapshotID string `json:"recovery_snapshot_id"`
}
