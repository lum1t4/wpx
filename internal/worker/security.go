package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type securityInstaller interface {
	InstallSecurity(context.Context, string) error
}

type securityApplier interface {
	ApplySecurity(context.Context, model.Site, model.SecuritySettings, string) error
}

func (p BrokerProvisioner) InstallSecurity(ctx context.Context, key string) error {
	return p.Client.Call(ctx, broker.OpSecurityInstall, key, struct{}{}, nil)
}

func (p BrokerProvisioner) ApplySecurity(ctx context.Context, site model.Site, settings model.SecuritySettings, key string) error {
	return p.Client.Call(ctx, broker.OpSecurityApply, key, broker.SecurityApplyRequest{Site: site, Settings: settings}, nil)
}

func (w *Worker) applySecurity(ctx context.Context, job store.Job) error {
	var payload struct {
		Generation int64 `json:"generation"`
	}
	if job.TargetType != "site" || json.Unmarshal([]byte(job.PayloadJSON), &payload) != nil || payload.Generation < 1 {
		return errors.New("security apply job is invalid")
	}
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return err
	}
	settings, err := w.Store.SiteSecuritySettings(ctx, site.ID)
	if err != nil {
		return err
	}
	if settings.Status != "queued" || settings.Generation != payload.Generation {
		return errors.New("security apply job no longer matches desired settings")
	}
	applier, ok := w.Provisioner.(securityApplier)
	if !ok {
		return errors.New("security settings are unavailable")
	}
	return applier.ApplySecurity(ctx, site, settings, job.IdempotencyKey)
}

func (w *Worker) installSecurity(ctx context.Context, job store.Job) error {
	if job.TargetType != "server" || job.TargetID != "security-defense" || job.PayloadJSON != "{}" {
		return errors.New("security installation job is invalid")
	}
	installer, ok := w.Provisioner.(securityInstaller)
	if !ok {
		return errors.New("security installation is unavailable")
	}
	return installer.InstallSecurity(ctx, job.IdempotencyKey)
}
