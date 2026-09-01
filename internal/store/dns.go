package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) CreateDNSProvider(ctx context.Context, actor User, provider model.DNSProvider) (model.DNSProvider, error) {
	provider.Name = strings.TrimSpace(provider.Name)
	provider.ZoneID = strings.TrimPrefix(strings.TrimSpace(provider.ZoneID), "/hostedzone/")
	if err := model.ValidateDNSProvider(provider); err != nil {
		return model.DNSProvider{}, err
	}
	provider.ID, provider.Status = mustID("dns_"), "queued"
	encoded, err := json.Marshal(provider)
	if err != nil {
		return model.DNSProvider{}, err
	}
	ciphertext, err := s.encrypt(encoded)
	if err != nil {
		return model.DNSProvider{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	jobID := mustID("job_")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.DNSProvider{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dns_providers(id,name,kind,status,config_ciphertext,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, provider.ID, provider.Name, provider.Kind, provider.Status, ciphertext, now, now); err != nil {
		return model.DNSProvider{}, fmt.Errorf("insert DNS provider: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "dns.provider_verify", "dns_provider", provider.ID, "queued", "waiting", 0, actor.ID, "dns.provider_verify:"+provider.ID, now, now); err != nil {
		return model.DNSProvider{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "dns_provider.created", "dns_provider", provider.ID, "success", now); err != nil {
		return model.DNSProvider{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.DNSProvider{}, err
	}
	return publicDNSProvider(provider), nil
}

func (s *Store) DNSProvider(ctx context.Context, id string) (model.DNSProvider, error) {
	var ciphertext []byte
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT config_ciphertext,status FROM dns_providers WHERE id=?`, id).Scan(&ciphertext, &status); err != nil {
		return model.DNSProvider{}, err
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		return model.DNSProvider{}, err
	}
	var provider model.DNSProvider
	if err := json.Unmarshal(plaintext, &provider); err != nil {
		return model.DNSProvider{}, err
	}
	provider.Status = status
	if err := model.ValidateDNSProvider(provider); err != nil {
		return model.DNSProvider{}, err
	}
	return provider, nil
}

func (s *Store) ListDNSProviders(ctx context.Context) ([]model.DNSProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,status FROM dns_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var providers []model.DNSProvider
	for rows.Next() {
		var provider model.DNSProvider
		if err := rows.Scan(&provider.ID, &provider.Name, &provider.Kind, &provider.Status); err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func publicDNSProvider(provider model.DNSProvider) model.DNSProvider {
	provider.APIToken, provider.AccessKey, provider.SecretKey, provider.SessionToken = "", "", "", ""
	return provider
}

func (s *Store) EnqueueDNSCertificate(ctx context.Context, actor User, siteID, providerID string, wildcard bool) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	provider, err := s.DNSProvider(ctx, providerID)
	if err != nil || provider.Status != "active" {
		return "", errors.New("DNS provider is not active")
	}
	payload, err := json.Marshal(struct {
		ProviderID string `json:"provider_id"`
		Wildcard   bool   `json:"wildcard"`
	}{ProviderID: provider.ID, Wildcard: wildcard})
	if err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sites SET tls_status='queued',updated_at=? WHERE id=? AND status='active'`, now, siteID)
	if err != nil {
		return "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", errors.New("certificate issuance requires an active site")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.certificate_dns", "site", siteID, "queued", "waiting", 0, actor.ID, "site.certificate_dns:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "certificate.dns_requested", "site", siteID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) CreateDNSRecord(ctx context.Context, actor User, record model.DNSRecord) (string, error) {
	provider, err := s.DNSProvider(ctx, record.ProviderID)
	if err != nil || provider.Status != "active" {
		return "", errors.New("DNS provider is not active")
	}
	record.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(record.Name), "."))
	record.Type = strings.ToUpper(strings.TrimSpace(record.Type))
	record.Value = strings.TrimSpace(record.Value)
	if err := model.ValidateDNSRecord(record, provider); err != nil {
		return "", err
	}
	if _, err := s.Site(ctx, record.SiteID); err != nil {
		return "", errors.New("site is unavailable")
	}
	record.ID, record.Status = mustID("rec_"), "queued"
	jobID := mustID("job_")
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dns_records(id,site_id,provider_id,name,type,value,ttl,proxied,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.SiteID, record.ProviderID, record.Name, record.Type, record.Value, record.TTL, record.Proxied, record.Status, now, now); err != nil {
		return "", fmt.Errorf("insert DNS record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "dns.record_apply", "dns_record", record.ID, "queued", "waiting", 0, actor.ID, "dns.record_apply:"+record.ID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "dns_record.requested", "dns_record", record.ID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) UpdateDNSRecord(ctx context.Context, actor User, siteID, recordID string, update model.DNSRecord) (string, error) {
	existing, err := s.DNSRecord(ctx, recordID)
	if err != nil || existing.SiteID != siteID || (existing.Status != "active" && existing.Status != "failed") {
		return "", errors.New("managed DNS record is unavailable for editing")
	}
	provider, err := s.DNSProvider(ctx, existing.ProviderID)
	if err != nil || provider.Status != "active" {
		return "", errors.New("DNS provider is not active")
	}
	update.ID, update.SiteID, update.ProviderID, update.RemoteID = existing.ID, existing.SiteID, existing.ProviderID, existing.RemoteID
	update.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(update.Name), "."))
	update.Type = strings.ToUpper(strings.TrimSpace(update.Type))
	update.Value = strings.TrimSpace(update.Value)
	if err := model.ValidateDNSRecord(update, provider); err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE dns_records SET name=?,type=?,value=?,ttl=?,proxied=?,status='queued',updated_at=? WHERE id=?`, update.Name, update.Type, update.Value, update.TTL, update.Proxied, now, existing.ID); err != nil {
		return "", fmt.Errorf("update DNS record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "dns.record_apply", "dns_record", existing.ID, "queued", "waiting", 0, actor.ID, "dns.record_apply:"+existing.ID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "dns_record.update_requested", "dns_record", existing.ID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) DeleteDNSRecord(ctx context.Context, actor User, siteID, recordID string) (string, error) {
	record, err := s.DNSRecord(ctx, recordID)
	if err != nil || record.SiteID != siteID || (record.Status != "active" && record.Status != "failed" && record.Status != "delete_failed") {
		return "", errors.New("managed DNS record is unavailable for deletion")
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// A failed create has no provider-side object. Removing it locally is both
	// complete and safer than inventing a remote identifier for a delete call.
	if record.RemoteID == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dns_records WHERE id=?`, record.ID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "dns_record.deleted_local", "dns_record", record.ID, "success", now); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dns_records SET status='deleting',updated_at=? WHERE id=?`, now, record.ID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "dns.record_delete", "dns_record", record.ID, "queued", "waiting", 0, actor.ID, "dns.record_delete:"+record.ID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "dns_record.delete_requested", "dns_record", record.ID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) DNSRecord(ctx context.Context, id string) (model.DNSRecord, error) {
	var record model.DNSRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,site_id,provider_id,name,type,value,ttl,proxied,remote_id,status FROM dns_records WHERE id=?`, id).
		Scan(&record.ID, &record.SiteID, &record.ProviderID, &record.Name, &record.Type, &record.Value, &record.TTL, &record.Proxied, &record.RemoteID, &record.Status)
	return record, err
}

func (s *Store) ListSiteDNSRecords(ctx context.Context, siteID string) ([]model.DNSRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,provider_id,name,type,value,ttl,proxied,remote_id,status FROM dns_records WHERE site_id=? ORDER BY name,type`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []model.DNSRecord
	for rows.Next() {
		var record model.DNSRecord
		if err := rows.Scan(&record.ID, &record.SiteID, &record.ProviderID, &record.Name, &record.Type, &record.Value, &record.TTL, &record.Proxied, &record.RemoteID, &record.Status); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
