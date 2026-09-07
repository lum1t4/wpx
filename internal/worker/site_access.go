package worker

import (
	"context"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type siteAccessProvisioner interface {
	ApplySiteAccess(context.Context, model.Site, model.SiteAccessSettings, string) error
}

func (p BrokerProvisioner) ApplySiteAccess(ctx context.Context, site model.Site, settings model.SiteAccessSettings, key string) error {
	return p.Client.Call(ctx, broker.OpApplySiteAccess, key, broker.ApplySiteAccessRequest{Site: site, Settings: settings}, nil)
}

func (w *Worker) applySiteAccess(ctx context.Context, job store.Job) error {
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "active" {
		return errors.New("access settings require an active site")
	}
	settings, err := w.Store.SiteAccess(ctx, site.ID)
	if err != nil {
		return err
	}
	if settings.Status != "pending" {
		return errors.New("site access job no longer matches pending desired state")
	}
	provisioner, ok := w.Provisioner.(siteAccessProvisioner)
	if !ok {
		return errors.New("site access management is unavailable")
	}
	return provisioner.ApplySiteAccess(ctx, site, settings, job.IdempotencyKey)
}
