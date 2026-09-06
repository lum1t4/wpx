// Package worker executes durable jobs outside HTTP request lifetimes. A worker
// may disappear at any instruction: job state and broker idempotency must make a
// later retry safe.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type SiteProvisioner interface {
	Provision(context.Context, model.Site, string) error
	Disable(context.Context, model.Site, bool, string) error
	ApplySnippets(context.Context, model.Site, model.SiteSnippets, string) error
	IssueCertificate(context.Context, model.Site, string) error
	IssueDNSCertificate(context.Context, model.Site, model.DNSProvider, bool, string) error
	InitBackup(context.Context, model.BackupTarget, string) error
	BackupSite(context.Context, model.Site, model.BackupTarget, model.BackupRetention, string) (broker.BackupSiteResult, error)
	RestoreSite(context.Context, model.Site, model.BackupTarget, string, string) (string, error)
	RestoreClone(context.Context, model.Site, model.Site, model.BackupTarget, string, string, string, string) error
	TestRestore(context.Context, model.Site, model.BackupTarget, string, string) error
	CreateStaging(context.Context, model.Site, model.Site, string, string, string) error
	DeployStaging(context.Context, model.Site, model.Site, model.BackupTarget, model.StagingSelection, string) (string, error)
	ApplyPerformance(context.Context, model.Site, string) error
	UpdateWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressUpdate, string) (string, error)
	CreateDatabase(context.Context, model.Database, string) error
	DeleteDatabase(context.Context, model.Database, string) error
	InstallDatabaseAdmin(context.Context, string) error
}

type DNSOperator interface {
	Verify(context.Context, model.DNSProvider) error
	Apply(context.Context, model.DNSProvider, model.DNSRecord) (string, error)
	Delete(context.Context, model.DNSProvider, model.DNSRecord) error
}

type phpVersionChanger interface {
	ChangePHPVersion(context.Context, model.Site, model.PHPVersionChange, string) error
}

type BrokerProvisioner struct{ Client broker.Client }

func (p BrokerProvisioner) Provision(ctx context.Context, site model.Site, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpProvisionSite, idempotencyKey, broker.ProvisionSiteRequest{Site: site}, nil)
}

func (p BrokerProvisioner) Disable(ctx context.Context, site model.Site, stopPHP bool, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpDisableSite, idempotencyKey, broker.DisableSiteRequest{Site: site, StopPHP: stopPHP}, nil)
}

func (p BrokerProvisioner) ChangePHPVersion(ctx context.Context, site model.Site, change model.PHPVersionChange, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpChangePHPVersion, idempotencyKey, broker.ChangePHPVersionRequest{Site: site, Change: change}, nil)
}

func (p BrokerProvisioner) ApplySnippets(ctx context.Context, site model.Site, snippets model.SiteSnippets, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpApplySiteSnippets, idempotencyKey, broker.ApplySiteSnippetsRequest{Site: site, Snippets: snippets}, nil)
}

func (p BrokerProvisioner) IssueCertificate(ctx context.Context, site model.Site, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpIssueCertificate, idempotencyKey, broker.IssueCertificateRequest{Site: site}, nil)
}

func (p BrokerProvisioner) IssueDNSCertificate(ctx context.Context, site model.Site, provider model.DNSProvider, wildcard bool, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpIssueDNSCertificate, idempotencyKey, broker.IssueDNSCertificateRequest{Site: site, Provider: provider, Wildcard: wildcard}, nil)
}

func (p BrokerProvisioner) InitBackup(ctx context.Context, target model.BackupTarget, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpBackupTargetInit, idempotencyKey, broker.BackupTargetInitRequest{Target: target}, nil)
}

func (p BrokerProvisioner) BackupSite(ctx context.Context, site model.Site, target model.BackupTarget, retention model.BackupRetention, idempotencyKey string) (broker.BackupSiteResult, error) {
	var result broker.BackupSiteResult
	err := p.Client.Call(ctx, broker.OpBackupSite, idempotencyKey, broker.BackupSiteRequest{Site: site, Target: target, Retention: retention, JobKey: idempotencyKey}, &result)
	return result, err
}

func (p BrokerProvisioner) RestoreSite(ctx context.Context, site model.Site, target model.BackupTarget, snapshotID, idempotencyKey string) (string, error) {
	var result broker.RestoreSiteResult
	err := p.Client.Call(ctx, broker.OpRestoreSite, idempotencyKey, broker.RestoreSiteRequest{Site: site, Target: target, SnapshotID: snapshotID, JobKey: idempotencyKey}, &result)
	return result.RollbackSnapshotID, err
}

func (p BrokerProvisioner) RestoreClone(ctx context.Context, source, target model.Site, backupTarget model.BackupTarget, snapshotID, username, password, idempotencyKey string) error {
	request := broker.RestoreCloneRequest{Source: source, Target: target, BackupTarget: backupTarget, SnapshotID: snapshotID, Username: username, Password: password, JobKey: idempotencyKey}
	return p.Client.Call(ctx, broker.OpRestoreClone, idempotencyKey, request, nil)
}

func (p BrokerProvisioner) TestRestore(ctx context.Context, site model.Site, target model.BackupTarget, snapshotID, idempotencyKey string) error {
	request := broker.TestRestoreRequest{Site: site, Target: target, SnapshotID: snapshotID, JobKey: idempotencyKey}
	return p.Client.Call(ctx, broker.OpTestRestore, idempotencyKey, request, nil)
}

func (p BrokerProvisioner) CreateStaging(ctx context.Context, source, target model.Site, username, password, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpCreateStaging, idempotencyKey, broker.CreateStagingRequest{Source: source, Target: target, Username: username, Password: password}, nil)
}

func (p BrokerProvisioner) DeployStaging(ctx context.Context, staging, production model.Site, target model.BackupTarget, selection model.StagingSelection, idempotencyKey string) (string, error) {
	var result broker.DeployStagingResult
	err := p.Client.Call(ctx, broker.OpDeployStaging, idempotencyKey, broker.DeployStagingRequest{Staging: staging, Production: production, BackupTarget: target, Selection: selection, JobKey: idempotencyKey}, &result)
	return result.RecoverySnapshotID, err
}

func (p BrokerProvisioner) ApplyPerformance(ctx context.Context, site model.Site, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpWordPressPerformance, idempotencyKey, broker.WordPressPerformanceRequest{Site: site}, nil)
}

func (p BrokerProvisioner) UpdateWordPress(ctx context.Context, site model.Site, target model.BackupTarget, update model.WordPressUpdate, idempotencyKey string) (string, error) {
	var result broker.WordPressUpdateResult
	err := p.Client.Call(ctx, broker.OpWordPressUpdate, idempotencyKey, broker.WordPressUpdateRequest{Site: site, BackupTarget: target, Update: update, JobKey: idempotencyKey}, &result)
	return result.RecoverySnapshotID, err
}

func (p BrokerProvisioner) CreateDatabase(ctx context.Context, database model.Database, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpDatabaseCreate, idempotencyKey, broker.DatabaseRequest{Database: database}, nil)
}

func (p BrokerProvisioner) DeleteDatabase(ctx context.Context, database model.Database, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpDatabaseDelete, idempotencyKey, broker.DatabaseRequest{Database: database}, nil)
}

func (p BrokerProvisioner) InstallDatabaseAdmin(ctx context.Context, idempotencyKey string) error {
	return p.Client.Call(ctx, broker.OpDatabaseAdminInstall, idempotencyKey, struct{}{}, nil)
}

type Worker struct {
	Store        *store.Store
	Provisioner  SiteProvisioner
	Logger       *slog.Logger
	DNS          DNSOperator
	PollInterval time.Duration
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Store == nil || w.Provisioner == nil {
		return errors.New("worker requires a store and provisioner")
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	if recovered, err := w.Store.RequeueInterruptedJobs(ctx); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	} else if recovered != 0 {
		w.Logger.Warn("requeued interrupted jobs", "count", recovered)
	}
	interval := w.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := w.Store.EnqueueDueBackups(ctx); err != nil {
			w.Logger.Error("enqueue scheduled backups", "error", err)
		}
		if _, err := w.Store.EnqueueDueRestoreTests(ctx); err != nil {
			w.Logger.Error("enqueue scheduled restore tests", "error", err)
		}
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			w.Logger.Error("process durable job", "error", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	job, found, err := w.Store.ClaimNextJob(ctx)
	if err != nil || !found {
		return false, err
	}
	var operationErr error
	resultJSON := "{}"
	switch job.Kind {
	case "database.create":
		var database model.Database
		database, operationErr = w.Store.Database(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.CreateDatabase(ctx, database, job.IdempotencyKey)
		}
	case "database.delete":
		var database model.Database
		database, operationErr = w.Store.Database(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.DeleteDatabase(ctx, database, job.IdempotencyKey)
		}
	case "database.admin_install":
		operationErr = w.Provisioner.InstallDatabaseAdmin(ctx, job.IdempotencyKey)
	case "site.domain_change":
		operationErr = w.changeDomain(ctx, job)
	case "site.delete":
		operationErr = w.deleteSite(ctx, job)
	case "site.provision":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.Provision(ctx, site, job.IdempotencyKey)
		}
	case "site.enable":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.Provision(ctx, site, job.IdempotencyKey)
		}
	case "site.disable":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		stopPHP := false
		if operationErr == nil && (site.Kind == model.WordPress || site.Kind == model.PHP) {
			var inUse bool
			inUse, operationErr = w.Store.OtherActiveSiteUsesPHP(ctx, site.ID, site.PHPVersion)
			stopPHP = !inUse
		}
		if operationErr == nil {
			operationErr = w.Provisioner.Disable(ctx, site, stopPHP, job.IdempotencyKey)
		}
	case "site.php_version":
		var change model.PHPVersionChange
		if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil {
			operationErr = errors.New("PHP version job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr != nil {
			break
		}
		if site.Status != "php_changing" || site.PHPVersion != change.PreviousVersion {
			operationErr = errors.New("PHP version job no longer matches the site")
			break
		}
		if operationErr = model.ValidatePHPVersionChange(site, change); operationErr != nil {
			break
		}
		changer, ok := w.Provisioner.(phpVersionChanger)
		if !ok {
			operationErr = errors.New("PHP version changes are unavailable")
			break
		}
		operationErr = changer.ChangePHPVersion(ctx, site, change, job.IdempotencyKey)
	case "site.config_apply":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		var snippets model.SiteSnippets
		if operationErr == nil {
			snippets, operationErr = w.Store.SiteSnippets(ctx, site.ID)
		}
		if operationErr == nil {
			operationErr = w.Provisioner.ApplySnippets(ctx, site, snippets, job.IdempotencyKey)
		}
	case "site.certificate":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.IssueCertificate(ctx, site, job.IdempotencyKey)
		}
	case "site.certificate_dns":
		var payload struct {
			ProviderID string `json:"provider_id"`
			Wildcard   bool   `json:"wildcard"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.ProviderID == "" {
			operationErr = errors.New("DNS certificate job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		var provider model.DNSProvider
		if operationErr == nil {
			provider, operationErr = w.Store.DNSProvider(ctx, payload.ProviderID)
		}
		if operationErr == nil {
			operationErr = w.Provisioner.IssueDNSCertificate(ctx, site, provider, payload.Wildcard, job.IdempotencyKey)
		}
	case "backup.target_init":
		var target model.BackupTarget
		target, operationErr = w.Store.BackupTarget(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.InitBackup(ctx, target, job.IdempotencyKey)
		}
	case "site.backup":
		var payload struct {
			TargetID  string                `json:"target_id"`
			Retention model.BackupRetention `json:"retention"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.TargetID == "" {
			operationErr = errors.New("backup job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr != nil {
			break
		}
		var target model.BackupTarget
		target, operationErr = w.Store.BackupTarget(ctx, payload.TargetID)
		if operationErr != nil {
			break
		}
		var backupResult broker.BackupSiteResult
		backupResult, operationErr = w.Provisioner.BackupSite(ctx, site, target, payload.Retention, job.IdempotencyKey)
		if operationErr == nil {
			encoded, err := json.Marshal(backupResult)
			if err != nil {
				operationErr = err
			} else {
				resultJSON = string(encoded)
			}
		}
	case "site.restore":
		var payload struct {
			TargetID   string `json:"target_id"`
			SnapshotID string `json:"snapshot_id"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.TargetID == "" || !model.ValidResticSnapshotID(payload.SnapshotID) {
			operationErr = errors.New("restore job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr != nil {
			break
		}
		var target model.BackupTarget
		target, operationErr = w.Store.BackupTarget(ctx, payload.TargetID)
		if operationErr != nil {
			break
		}
		var rollbackID string
		rollbackID, operationErr = w.Provisioner.RestoreSite(ctx, site, target, payload.SnapshotID, job.IdempotencyKey)
		if operationErr == nil {
			encoded, err := json.Marshal(broker.RestoreSiteResult{RollbackSnapshotID: rollbackID})
			if err != nil {
				operationErr = err
			} else {
				resultJSON = string(encoded)
			}
		}
	case "site.restore_clone":
		var payload struct {
			SourceID       string `json:"source_id"`
			BackupTargetID string `json:"target_id"`
			SnapshotID     string `json:"snapshot_id"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || model.ValidateSiteID(payload.SourceID) != nil || payload.BackupTargetID == "" || !model.ValidResticSnapshotID(payload.SnapshotID) {
			operationErr = errors.New("restore clone job payload is invalid")
			break
		}
		var source, target model.Site
		source, operationErr = w.Store.Site(ctx, payload.SourceID)
		if operationErr == nil {
			target, operationErr = w.Store.Site(ctx, job.TargetID)
		}
		var backupTarget model.BackupTarget
		if operationErr == nil {
			backupTarget, operationErr = w.Store.BackupTarget(ctx, payload.BackupTargetID)
		}
		username, password := "", ""
		if operationErr == nil && target.Environment == "staging" {
			username, password, operationErr = w.Store.StagingCredential(ctx, target.ID)
		}
		if operationErr == nil {
			operationErr = w.Provisioner.RestoreClone(ctx, source, target, backupTarget, payload.SnapshotID, username, password, job.IdempotencyKey)
		}
	case "site.restore_test":
		var payload struct {
			TargetID   string `json:"target_id"`
			SnapshotID string `json:"snapshot_id"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.TargetID == "" || !model.ValidResticSnapshotID(payload.SnapshotID) {
			operationErr = errors.New("restore test job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		var target model.BackupTarget
		if operationErr == nil {
			target, operationErr = w.Store.BackupTarget(ctx, payload.TargetID)
		}
		if operationErr == nil {
			operationErr = w.Provisioner.TestRestore(ctx, site, target, payload.SnapshotID, job.IdempotencyKey)
		}
	case "wordpress.staging_create", "wordpress.staging_sync":
		var payload struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || model.ValidateSiteID(payload.SourceID) != nil {
			operationErr = errors.New("staging job payload is invalid")
			break
		}
		var source, target model.Site
		source, operationErr = w.Store.Site(ctx, payload.SourceID)
		if operationErr == nil {
			target, operationErr = w.Store.Site(ctx, job.TargetID)
		}
		if operationErr == nil {
			var username, password string
			username, password, operationErr = w.Store.StagingCredential(ctx, target.ID)
			if operationErr == nil {
				operationErr = w.Provisioner.CreateStaging(ctx, source, target, username, password, job.IdempotencyKey)
			}
		}
	case "wordpress.staging_deploy":
		var payload struct {
			StagingID string                 `json:"staging_id"`
			TargetID  string                 `json:"target_id"`
			Selection model.StagingSelection `json:"selection"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || model.ValidateSiteID(payload.StagingID) != nil || payload.TargetID == "" {
			operationErr = errors.New("staging deployment payload is invalid")
			break
		}
		var staging, production model.Site
		staging, operationErr = w.Store.Site(ctx, payload.StagingID)
		if operationErr == nil {
			production, operationErr = w.Store.Site(ctx, job.TargetID)
		}
		var target model.BackupTarget
		if operationErr == nil {
			target, operationErr = w.Store.BackupTarget(ctx, payload.TargetID)
		}
		var recoveryID string
		if operationErr == nil {
			recoveryID, operationErr = w.Provisioner.DeployStaging(ctx, staging, production, target, payload.Selection, job.IdempotencyKey)
		}
		if operationErr == nil {
			encoded, err := json.Marshal(broker.DeployStagingResult{RecoverySnapshotID: recoveryID})
			if err != nil {
				operationErr = err
			} else {
				resultJSON = string(encoded)
			}
		}
	case "wordpress.performance_apply":
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.Provisioner.ApplyPerformance(ctx, site, job.IdempotencyKey)
		}
	case "wordpress.update":
		var payload struct {
			TargetID string                `json:"target_id"`
			Update   model.WordPressUpdate `json:"update"`
		}
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.TargetID == "" || model.ValidateWordPressUpdate(payload.Update) != nil {
			operationErr = errors.New("WordPress update job payload is invalid")
			break
		}
		var site model.Site
		site, operationErr = w.Store.Site(ctx, job.TargetID)
		var target model.BackupTarget
		if operationErr == nil {
			target, operationErr = w.Store.BackupTarget(ctx, payload.TargetID)
		}
		var recoveryID string
		if operationErr == nil {
			recoveryID, operationErr = w.Provisioner.UpdateWordPress(ctx, site, target, payload.Update, job.IdempotencyKey)
		}
		if operationErr == nil {
			encoded, err := json.Marshal(broker.WordPressUpdateResult{RecoverySnapshotID: recoveryID})
			if err != nil {
				operationErr = err
			} else {
				resultJSON = string(encoded)
			}
		}
	case "dns.provider_verify":
		if w.DNS == nil {
			operationErr = errors.New("DNS operations are unavailable")
			break
		}
		var provider model.DNSProvider
		provider, operationErr = w.Store.DNSProvider(ctx, job.TargetID)
		if operationErr == nil {
			operationErr = w.DNS.Verify(ctx, provider)
		}
	case "dns.record_apply":
		if w.DNS == nil {
			operationErr = errors.New("DNS operations are unavailable")
			break
		}
		record, err := w.Store.DNSRecord(ctx, job.TargetID)
		operationErr = err
		var provider model.DNSProvider
		if operationErr == nil {
			provider, operationErr = w.Store.DNSProvider(ctx, record.ProviderID)
		}
		var remoteID string
		if operationErr == nil {
			remoteID, operationErr = w.DNS.Apply(ctx, provider, record)
		}
		if operationErr == nil {
			encoded, err := json.Marshal(map[string]string{"remote_id": remoteID})
			if err != nil {
				operationErr = err
			} else {
				resultJSON = string(encoded)
			}
		}
	case "dns.record_delete":
		if w.DNS == nil {
			operationErr = errors.New("DNS operations are unavailable")
			break
		}
		record, err := w.Store.DNSRecord(ctx, job.TargetID)
		operationErr = err
		var provider model.DNSProvider
		if operationErr == nil {
			provider, operationErr = w.Store.DNSProvider(ctx, record.ProviderID)
		}
		if operationErr == nil {
			operationErr = w.DNS.Delete(ctx, provider, record)
		}
	default:
		operationErr = fmt.Errorf("unsupported job kind %q", job.Kind)
	}
	if (job.Kind == "site.php_version" || job.Kind == "site.domain_change" || job.Kind == "site.delete") && (errors.Is(operationErr, broker.ErrOutcomeUnknown) || errors.Is(operationErr, broker.ErrUnavailable)) {
		detail := "Waiting for broker confirmation: " + operationErr.Error()
		var retryErr error
		if job.Kind == "site.php_version" {
			retryErr = w.Store.RetryPHPVersionChange(ctx, job.ID, detail)
		} else {
			retryErr = w.Store.RetrySiteLifecycleJob(ctx, job.ID, detail)
		}
		if retryErr != nil {
			return false, fmt.Errorf("retry %s job %s: %w", job.Kind, job.ID, retryErr)
		}
		// The privileged operation can outlive a lost socket connection. Keep
		// the reservation and let Run wait for its next poll before replaying.
		return false, fmt.Errorf("%s job %s is awaiting broker confirmation: %w", job.Kind, job.ID, operationErr)
	}
	if err := w.Store.FinishJob(ctx, job, resultJSON, operationErr); err != nil {
		return true, fmt.Errorf("finish job %s: %w", job.ID, err)
	}
	if operationErr != nil {
		w.Logger.Error("job failed", "job", job.ID, "kind", job.Kind, "target", job.TargetID, "error", operationErr)
	}
	return true, nil
}
