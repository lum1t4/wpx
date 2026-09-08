package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type wordpressSearchReplaceProvisioner interface {
	SearchReplaceWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressSearchReplace, bool, string) (broker.WordPressSearchReplaceResult, error)
}

func (p BrokerProvisioner) SearchReplaceWordPress(ctx context.Context, site model.Site, target model.BackupTarget, change model.WordPressSearchReplace, dryRun bool, idempotencyKey string) (broker.WordPressSearchReplaceResult, error) {
	var result broker.WordPressSearchReplaceResult
	err := p.Client.Call(ctx, broker.OpWordPressSearchReplace, idempotencyKey, broker.WordPressSearchReplaceRequest{Site: site, BackupTarget: target, Change: change, DryRun: dryRun}, &result)
	return result, err
}

// processWordPressSearchReplace is called from the shared durable worker switch.
func (w *Worker) processWordPressSearchReplace(ctx context.Context, job store.Job) (string, error) {
	provisioner, ok := w.Provisioner.(wordpressSearchReplaceProvisioner)
	if !ok {
		return "{}", errors.New("WordPress search and replace is unavailable")
	}
	targetID, change, err := w.Store.WordPressSearchReplaceJob(job)
	if err != nil {
		return "{}", err
	}
	site, err := w.Store.Site(ctx, job.TargetID)
	if err != nil {
		return "{}", err
	}
	target, err := w.Store.BackupTarget(ctx, targetID)
	if err != nil {
		return "{}", err
	}
	result, operationErr := provisioner.SearchReplaceWordPress(ctx, site, target, change, false, job.IdempotencyKey)
	encoded, err := json.Marshal(result)
	if err != nil {
		return "{}", err
	}
	return string(encoded), operationErr
}
