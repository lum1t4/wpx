package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type cronProvisioner interface {
	ApplyCron(context.Context, model.Site, model.CronSchedule, bool, string) error
	ApplyWordPressCron(context.Context, model.Site, model.WordPressCronSetting, string) error
}

func (p BrokerProvisioner) ApplyCron(ctx context.Context, site model.Site, schedule model.CronSchedule, remove bool, key string) error {
	return p.Client.Call(ctx, broker.OpCronApply, key, broker.ApplyCronRequest{Site: site, Schedule: schedule, Delete: remove}, nil)
}
func (p BrokerProvisioner) ApplyWordPressCron(ctx context.Context, site model.Site, setting model.WordPressCronSetting, key string) error {
	return p.Client.Call(ctx, broker.OpWordPressCronApply, key, broker.ApplyWordPressCronRequest{Site: site, Setting: setting}, nil)
}

func (w *Worker) processCron(ctx context.Context, job store.Job) error {
	manager, ok := w.Provisioner.(cronProvisioner)
	if !ok {
		return errors.New("cron operations are unavailable")
	}
	var payload store.CronJobPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return errors.New("cron job payload is invalid")
	}
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return err
	}
	if payload.Schedule != nil {
		return manager.ApplyCron(ctx, site, *payload.Schedule, payload.Action == "delete", job.IdempotencyKey)
	}
	if payload.Setting != nil {
		return manager.ApplyWordPressCron(ctx, site, *payload.Setting, job.IdempotencyKey)
	}
	return errors.New("cron job payload is incomplete")
}
