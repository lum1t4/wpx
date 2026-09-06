package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type domainChanger interface {
	ChangeDomain(context.Context, model.Site, model.DomainChange, string) error
}

type siteDeleter interface {
	DeleteSite(context.Context, model.Site, bool, string) error
}

func (p BrokerProvisioner) ChangeDomain(ctx context.Context, site model.Site, change model.DomainChange, key string) error {
	return p.Client.Call(ctx, broker.OpChangeDomain, key, broker.ChangeDomainRequest{Site: site, Change: change}, nil)
}

func (p BrokerProvisioner) DeleteSite(ctx context.Context, site model.Site, stopPHP bool, key string) error {
	return p.Client.Call(ctx, broker.OpDeleteSite, key, broker.DeleteSiteRequest{Site: site, StopPHP: stopPHP}, nil)
}

func (w *Worker) changeDomain(ctx context.Context, job store.Job) error {
	var change model.DomainChange
	if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil {
		return errors.New("domain change job payload is invalid")
	}
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "domain_changing" {
		return errors.New("domain change job no longer matches the site's reserved state")
	}
	if err := model.ValidateDomainChange(site, change); err != nil {
		return err
	}
	changer, ok := w.Provisioner.(domainChanger)
	if !ok {
		return errors.New("domain changes are unavailable")
	}
	return changer.ChangeDomain(ctx, site, change, job.IdempotencyKey)
}

func (w *Worker) deleteSite(ctx context.Context, job store.Job) error {
	var payload store.SiteDeletion
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.Site.ID != job.TargetID {
		return errors.New("site deletion job payload is invalid")
	}
	if err := model.ValidateSite(payload.Site); err != nil {
		return err
	}
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "deleting" || site != payload.Site {
		return errors.New("site deletion job no longer matches the site's reserved state")
	}
	deleter, ok := w.Provisioner.(siteDeleter)
	if !ok {
		return errors.New("site deletion is unavailable")
	}
	return deleter.DeleteSite(ctx, payload.Site, payload.StopPHP, job.IdempotencyKey)
}
