package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

const maxRequestBytes = 2 << 20

type Server struct {
	SocketPath  string
	SiteRoot    string
	AllowedUID  uint32
	SocketGID   int
	Provisioner SiteProvisioner
	WordPress   WordPressOperator
	Files       FileOperator
	Backups     BackupOperator
	Metrics     ObservabilityOperator

	mu       sync.Mutex
	listener net.Listener
}

type SiteProvisioner interface {
	Provision(context.Context, model.Site) error
	Disable(context.Context, model.Site, bool) error
	ApplySnippets(context.Context, model.Site, model.SiteSnippets) error
	IssueCertificate(context.Context, model.Site) error
	IssueDNSCertificate(context.Context, model.Site, model.DNSProvider, bool) error
	CreateStaging(context.Context, model.Site, model.Site, string, string) error
	InspectStaging(context.Context, model.Site, model.Site) (StagingInspection, error)
	DeployStaging(context.Context, model.Site, model.Site, model.BackupTarget, StagingSelection, string) (string, error)
	ApplyPerformance(context.Context, model.Site) error
	UpdateWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressUpdate, string) (string, error)
}

type WordPressOperator interface {
	MagicLogin(context.Context, model.Site) (string, time.Time, error)
	Inventory(context.Context, model.Site) (WordPressInventoryResult, error)
	Health(context.Context, model.Site) WordPressHealthResult
	Plugins(context.Context, model.Site) ([]WordPressPlugin, error)
	SetPlugin(context.Context, model.Site, string, bool) error
}

type FileOperator interface {
	ListFiles(context.Context, model.Site, string) ([]FileEntry, error)
	ReadFile(context.Context, model.Site, string) (string, error)
	WriteFile(context.Context, model.Site, string, string) error
}

type BackupOperator interface {
	InitBackup(context.Context, model.BackupTarget) error
	BackupSite(context.Context, model.Site, model.BackupTarget, model.BackupRetention, string) (BackupSiteResult, error)
	RestoreSite(context.Context, model.Site, model.BackupTarget, string, string) (string, error)
	RestoreClone(context.Context, model.Site, model.Site, model.BackupTarget, string, string, string, string) error
	TestRestore(context.Context, model.Site, model.BackupTarget, string, string) error
}

type ObservabilityOperator interface {
	Observability(context.Context, model.Site) (SiteObservabilityResult, error)
}

func (s *Server) Run(ctx context.Context) error {
	if !filepath.IsAbs(s.SocketPath) || !filepath.IsAbs(s.SiteRoot) || s.SiteRoot == "/" {
		return errors.New("broker paths must be absolute and site root must not be /")
	}
	if err := ensureTrustedDirectory(s.SiteRoot); err != nil {
		return fmt.Errorf("prepare site root: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0750); err != nil {
		return fmt.Errorf("prepare socket directory: %w", err)
	}
	if st, err := os.Lstat(s.SocketPath); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refuse to replace non-socket %s", s.SocketPath)
		}
		if err := os.Remove(s.SocketPath); err != nil {
			return fmt.Errorf("remove stale broker socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on broker socket: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	defer func() {
		listener.Close()
		os.Remove(s.SocketPath)
	}()
	if err := os.Chmod(s.SocketPath, 0660); err != nil {
		return fmt.Errorf("set broker socket mode: %w", err)
	}
	if s.SocketGID >= 0 {
		if err := os.Chown(s.SocketPath, 0, s.SocketGID); err != nil {
			return fmt.Errorf("set broker socket group: %w", err)
		}
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept broker connection: %w", err)
		}
		go s.handle(conn)
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil || uid != s.AllowedUID {
		writeResponse(conn, Response{Version: ProtocolVersion, OK: false, Error: "unauthorized broker peer"})
		return
	}
	limited := io.LimitReader(conn, maxRequestBytes+1)
	line, err := bufio.NewReader(limited).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		writeResponse(conn, Response{Version: ProtocolVersion, OK: false, Error: "invalid broker request"})
		return
	}
	if len(line) > maxRequestBytes {
		writeResponse(conn, Response{Version: ProtocolVersion, OK: false, Error: "broker request too large"})
		return
	}
	var request Request
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Version != ProtocolVersion || request.ID == "" || request.IdempotencyKey == "" {
		writeResponse(conn, Response{Version: ProtocolVersion, ID: request.ID, OK: false, Error: "invalid broker envelope"})
		return
	}
	response := s.dispatch(request)
	writeResponse(conn, response)
}

func (s *Server) dispatch(request Request) Response {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	switch request.Operation {
	case OpProbe:
		response.OK = true
		response.Result = json.RawMessage(`{"ready":true}`)
	case OpEnsureSiteRoot:
		var payload EnsureSiteRootRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSiteID(payload.SiteID) != nil {
			response.Error = "invalid site root request"
			return response
		}
		created, err := s.ensureSiteRoot(payload.SiteID)
		if err != nil {
			response.Error = "site root operation failed"
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(EnsureSiteRootResult{Created: created})
	case OpProvisionSite:
		var payload ProvisionSiteRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid site provisioning request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "site provisioning is unavailable"
			return response
		}
		if err := s.Provisioner.Provision(context.Background(), payload.Site); err != nil {
			response.Error = "site provisioning failed: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"provisioned":true}`)
	case OpDisableSite:
		var payload DisableSiteRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || (payload.Site.Status != "disabling" && payload.Site.Status != "disable_failed") {
			response.Error = "invalid site disable request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "site lifecycle operations are unavailable"
			return response
		}
		if err := s.Provisioner.Disable(context.Background(), payload.Site, payload.StopPHP); err != nil {
			response.Error = "disable site: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"disabled":true}`)
	case OpChangeDomain:
		return s.changeDomain(request, response)
	case OpDeleteSite:
		return s.deleteSite(request, response)
	case OpChangePHPVersion:
		var payload ChangePHPVersionRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidatePHPVersionChange(payload.Site, payload.Change) != nil || payload.Site.Status != "php_changing" {
			response.Error = "invalid PHP version change request"
			return response
		}
		manager, ok := s.Provisioner.(interface {
			ChangePHPVersion(context.Context, model.Site, model.PHPVersionChange) error
		})
		if !ok {
			response.Error = "PHP version changes are unavailable"
			return response
		}
		if err := manager.ChangePHPVersion(context.Background(), payload.Site, payload.Change); err != nil {
			response.Error = "change PHP version: " + err.Error()
			var changeErr *model.PHPVersionChangeError
			if errors.As(err, &changeErr) {
				response.Result, _ = json.Marshal(ChangePHPVersionResult{PreviousRestored: changeErr.PreviousRestored})
			}
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"changed":true}`)
	case OpApplySiteSnippets:
		var payload ApplySiteSnippetsRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateSiteSnippets(payload.Site, payload.Snippets) != nil || (payload.Site.Status != "active" && payload.Site.Status != "disabled") {
			response.Error = "invalid site snippet request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "site configuration operations are unavailable"
			return response
		}
		if err := s.Provisioner.ApplySnippets(context.Background(), payload.Site, payload.Snippets); err != nil {
			response.Error = "apply site snippets: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"applied":true}`)
	case OpCreateStaging:
		var payload CreateStagingRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Source) != nil || model.ValidateSite(payload.Target) != nil || payload.Source.Kind != model.WordPress || payload.Source.Environment != "production" || payload.Target.Kind != model.WordPress || payload.Target.Environment != "staging" || payload.Target.ParentSiteID != payload.Source.ID || payload.Username != "wpx" || len(payload.Password) < 24 || len(payload.Password) > 128 {
			response.Error = "invalid staging request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "staging operations are unavailable"
			return response
		}
		if err := s.Provisioner.CreateStaging(context.Background(), payload.Source, payload.Target, payload.Username, payload.Password); err != nil {
			response.Error = "create staging: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"created":true}`)
	case OpDeployStaging:
		var payload DeployStagingRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Staging) != nil || model.ValidateSite(payload.Production) != nil || model.ValidateBackupTarget(payload.BackupTarget) != nil || payload.Staging.Environment != "staging" || payload.Staging.ParentSiteID != payload.Production.ID || payload.Production.Environment != "production" || model.ValidateStagingSelection(payload.Selection) != nil || payload.JobKey == "" || len(payload.JobKey) > 160 {
			response.Error = "invalid staging deployment request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "staging deployment is unavailable"
			return response
		}
		recoveryID, err := s.Provisioner.DeployStaging(context.Background(), payload.Staging, payload.Production, payload.BackupTarget, payload.Selection, payload.JobKey)
		if err != nil {
			response.Error = "deploy staging: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(DeployStagingResult{RecoverySnapshotID: recoveryID})
	case OpInspectStaging:
		var payload InspectStagingRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Staging) != nil || model.ValidateSite(payload.Production) != nil || payload.Staging.Kind != model.WordPress || payload.Staging.Environment != "staging" || payload.Staging.ParentSiteID != payload.Production.ID || payload.Production.Kind != model.WordPress || payload.Production.Environment != "production" {
			response.Error = "invalid staging inspection request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "staging inspection is unavailable"
			return response
		}
		inspection, err := s.Provisioner.InspectStaging(context.Background(), payload.Staging, payload.Production)
		if err != nil {
			response.Error = "inspect staging: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(inspection)
	case OpSiteObservability:
		var payload SiteObservabilityRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid observability request"
			return response
		}
		if s.Metrics == nil {
			response.Error = "site observability is unavailable"
			return response
		}
		result, err := s.Metrics.Observability(context.Background(), payload.Site)
		if err != nil {
			response.Error = "read site observability: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(result)
	case OpWordPressPerformance:
		var payload WordPressPerformanceRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress {
			response.Error = "invalid WordPress performance request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "WordPress performance operations are unavailable"
			return response
		}
		if err := s.Provisioner.ApplyPerformance(context.Background(), payload.Site); err != nil {
			response.Error = "apply WordPress performance settings: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"applied":true}`)
	case OpWordPressUpdate:
		var payload WordPressUpdateRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress || payload.Site.Status != "active" || model.ValidateBackupTarget(payload.BackupTarget) != nil || model.ValidateWordPressUpdate(payload.Update) != nil || payload.JobKey == "" || len(payload.JobKey) > 160 {
			response.Error = "invalid WordPress update request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "WordPress update operations are unavailable"
			return response
		}
		recoveryID, err := s.Provisioner.UpdateWordPress(context.Background(), payload.Site, payload.BackupTarget, payload.Update, payload.JobKey)
		if err != nil {
			response.Error = "update WordPress: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(WordPressUpdateResult{RecoverySnapshotID: recoveryID})
	case OpWordPressLogin:
		var payload WordPressLoginRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress {
			response.Error = "invalid WordPress login request"
			return response
		}
		if s.WordPress == nil {
			response.Error = "WordPress operations are unavailable"
			return response
		}
		loginURL, expiresAt, err := s.WordPress.MagicLogin(context.Background(), payload.Site)
		if err != nil {
			response.Error = "create WordPress login: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(WordPressLoginResult{URL: loginURL, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)})
	case OpWordPressInventory:
		var payload WordPressPluginsRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress || payload.Site.Status != "active" {
			response.Error = "invalid WordPress inventory request"
			return response
		}
		if s.WordPress == nil {
			response.Error = "WordPress operations are unavailable"
			return response
		}
		result, err := s.WordPress.Inventory(context.Background(), payload.Site)
		if err != nil {
			response.Error = "load WordPress inventory: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(result)
	case OpWordPressHealth:
		var payload WordPressPluginsRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress || payload.Site.Status != "active" {
			response.Error = "invalid WordPress health request"
			return response
		}
		if s.WordPress == nil {
			response.Error = "WordPress operations are unavailable"
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(s.WordPress.Health(context.Background(), payload.Site))
	case OpIssueCertificate:
		var payload IssueCertificateRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid certificate request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "certificate operations are unavailable"
			return response
		}
		if err := s.Provisioner.IssueCertificate(context.Background(), payload.Site); err != nil {
			response.Error = "issue certificate: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"issued":true}`)
	case OpIssueDNSCertificate:
		var payload IssueDNSCertificateRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateDNSProvider(payload.Provider) != nil || payload.Provider.Status != "active" {
			response.Error = "invalid DNS certificate request"
			return response
		}
		if s.Provisioner == nil {
			response.Error = "certificate operations are unavailable"
			return response
		}
		if err := s.Provisioner.IssueDNSCertificate(context.Background(), payload.Site, payload.Provider, payload.Wildcard); err != nil {
			response.Error = "issue DNS certificate: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"issued":true}`)
	case OpWordPressPlugins:
		var payload WordPressPluginsRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress {
			response.Error = "invalid WordPress plugin request"
			return response
		}
		if s.WordPress == nil {
			response.Error = "WordPress operations are unavailable"
			return response
		}
		plugins, err := s.WordPress.Plugins(context.Background(), payload.Site)
		if err != nil {
			response.Error = "inspect WordPress plugins: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(WordPressPluginsResult{Plugins: plugins})
	case OpWordPressPluginSet:
		var payload WordPressPluginSetRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress {
			response.Error = "invalid WordPress plugin mutation"
			return response
		}
		if s.WordPress == nil {
			response.Error = "WordPress operations are unavailable"
			return response
		}
		if err := s.WordPress.SetPlugin(context.Background(), payload.Site, payload.Plugin, payload.Active); err != nil {
			response.Error = "change WordPress plugin: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"changed":true}`)
	case OpFileList:
		var payload FileRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid file-list request"
			return response
		}
		if s.Files == nil {
			response.Error = "file operations are unavailable"
			return response
		}
		entries, err := s.Files.ListFiles(context.Background(), payload.Site, payload.Path)
		if err != nil {
			response.Error = "list files: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(FileListResult{Entries: entries})
	case OpFileRead:
		var payload FileRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid file-read request"
			return response
		}
		if s.Files == nil {
			response.Error = "file operations are unavailable"
			return response
		}
		content, err := s.Files.ReadFile(context.Background(), payload.Site, payload.Path)
		if err != nil {
			response.Error = "read file: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(FileReadResult{Content: content})
	case OpFileWrite:
		var payload FileWriteRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil {
			response.Error = "invalid file-write request"
			return response
		}
		if s.Files == nil {
			response.Error = "file operations are unavailable"
			return response
		}
		if err := s.Files.WriteFile(context.Background(), payload.Site, payload.Path, payload.Content); err != nil {
			response.Error = "write file: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"saved":true}`)
	case OpBackupTargetInit:
		var payload BackupTargetInitRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateBackupTarget(payload.Target) != nil {
			response.Error = "invalid backup target request"
			return response
		}
		if s.Backups == nil {
			response.Error = "backup operations are unavailable"
			return response
		}
		if err := s.Backups.InitBackup(context.Background(), payload.Target); err != nil {
			response.Error = "initialize backup target: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"initialized":true}`)
	case OpBackupSite:
		var payload BackupSiteRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateBackupTarget(payload.Target) != nil || model.ValidateBackupRetention(payload.Retention, true) != nil || payload.JobKey == "" || len(payload.JobKey) > 160 {
			response.Error = "invalid site backup request"
			return response
		}
		if s.Backups == nil {
			response.Error = "backup operations are unavailable"
			return response
		}
		result, err := s.Backups.BackupSite(context.Background(), payload.Site, payload.Target, payload.Retention, payload.JobKey)
		if err != nil {
			response.Error = "back up site: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(result)
	case OpRestoreSite:
		var payload RestoreSiteRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateBackupTarget(payload.Target) != nil || !model.ValidResticSnapshotID(payload.SnapshotID) || payload.JobKey == "" || len(payload.JobKey) > 160 {
			response.Error = "invalid site restore request"
			return response
		}
		if s.Backups == nil {
			response.Error = "backup operations are unavailable"
			return response
		}
		rollbackID, err := s.Backups.RestoreSite(context.Background(), payload.Site, payload.Target, payload.SnapshotID, payload.JobKey)
		if err != nil {
			response.Error = "restore site: " + err.Error()
			return response
		}
		response.OK = true
		response.Result, _ = json.Marshal(RestoreSiteResult{RollbackSnapshotID: rollbackID})
	case OpRestoreClone:
		var payload RestoreCloneRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Source) != nil || model.ValidateSite(payload.Target) != nil || model.ValidateBackupTarget(payload.BackupTarget) != nil || !model.ValidResticSnapshotID(payload.SnapshotID) || payload.Target.Status != "queued" || payload.Target.Kind != payload.Source.Kind || payload.Target.ID == payload.Source.ID || payload.JobKey == "" || len(payload.JobKey) > 160 || payload.Target.Environment == "staging" && (payload.Username != "wpx" || len(payload.Password) < 24 || len(payload.Password) > 128) {
			response.Error = "invalid restore clone request"
			return response
		}
		if payload.Target.Environment == "staging" && (payload.Target.Kind != model.WordPress || payload.Target.ParentSiteID == "") {
			response.Error = "invalid restore staging relationship"
			return response
		}
		if s.Backups == nil {
			response.Error = "restore clone operations are unavailable"
			return response
		}
		if err := s.Backups.RestoreClone(context.Background(), payload.Source, payload.Target, payload.BackupTarget, payload.SnapshotID, payload.Username, payload.Password, payload.JobKey); err != nil {
			response.Error = "restore into new site: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"restored":true}`)
	case OpTestRestore:
		var payload TestRestoreRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Status != "active" || model.ValidateBackupTarget(payload.Target) != nil || !model.ValidResticSnapshotID(payload.SnapshotID) || payload.JobKey == "" || len(payload.JobKey) > 160 {
			response.Error = "invalid restore test request"
			return response
		}
		if s.Backups == nil {
			response.Error = "restore testing is unavailable"
			return response
		}
		if err := s.Backups.TestRestore(context.Background(), payload.Site, payload.Target, payload.SnapshotID, payload.JobKey); err != nil {
			response.Error = "test restore: " + err.Error()
			return response
		}
		response.OK = true
		response.Result = json.RawMessage(`{"tested":true}`)
	default:
		response.Error = "unsupported broker operation"
	}
	return response
}

func (s *Server) ensureSiteRoot(siteID string) (bool, error) {
	target := filepath.Join(s.SiteRoot, siteID)
	// Validate the containment after joining even though site IDs are restricted.
	// This defense must survive future changes to identifier policy.
	rel, err := filepath.Rel(s.SiteRoot, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false, errors.New("derived site path escapes root")
	}
	if st, err := os.Lstat(target); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("site root exists but is not a real directory")
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Mkdir(target, 0750); err != nil {
		return false, err
	}
	return true, nil
}

func ensureTrustedDirectory(path string) error {
	if err := os.MkdirAll(path, 0750); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("trusted root is not a real directory")
	}
	return nil
}

func writeResponse(w io.Writer, response Response) {
	_ = json.NewEncoder(w).Encode(response)
}
