package broker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

const OpWordPressSearchReplace Operation = "wordpress.search_replace"

type WordPressSearchReplaceRequest struct {
	Site         model.Site                   `json:"site"`
	BackupTarget model.BackupTarget           `json:"backup_target,omitempty"`
	Change       model.WordPressSearchReplace `json:"change"`
	DryRun       bool                         `json:"dry_run"`
}

type WordPressSearchReplaceTableResult struct {
	Name         string `json:"name"`
	Replacements int64  `json:"replacements"`
}

type WordPressSearchReplaceResult struct {
	Tables             int                                 `json:"tables"`
	Replacements       int64                               `json:"replacements"`
	TableResults       []WordPressSearchReplaceTableResult `json:"table_results,omitempty"`
	RecoverySnapshotID string                              `json:"recovery_snapshot_id,omitempty"`
}

type wordpressSearchReplaceOperator interface {
	SearchReplaceWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressSearchReplace, bool, string) (WordPressSearchReplaceResult, error)
}

// dispatchWordPressSearchReplace is registered by the shared broker dispatcher.
// It uses the envelope idempotency key as the only host journal key.
func (s *Server) dispatchWordPressSearchReplace(request Request) (Response, bool) {
	if request.Operation != OpWordPressSearchReplace {
		return Response{}, false
	}
	response := Response{Version: ProtocolVersion, ID: request.ID}
	var payload WordPressSearchReplaceRequest
	if decodeLifecyclePayload(request.Payload, &payload) != nil || !validLifecycleKey(request.IdempotencyKey) || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress || payload.Site.Status != "active" || model.ValidateWordPressSearchReplace(payload.Change) != nil {
		response.Error = "invalid WordPress search and replace request"
		return response, true
	}
	if payload.DryRun {
		if payload.BackupTarget != (model.BackupTarget{}) {
			response.Error = "invalid WordPress search and replace preview request"
			return response, true
		}
	} else if model.ValidateBackupTarget(payload.BackupTarget) != nil || payload.BackupTarget.Status != "active" {
		response.Error = "invalid WordPress search and replace apply request"
		return response, true
	}
	operator, ok := s.Provisioner.(wordpressSearchReplaceOperator)
	if !ok {
		response.Error = "WordPress search and replace is unavailable"
		return response, true
	}
	timeout := 30 * time.Minute
	if payload.DryRun {
		timeout = 12 * time.Second
	}
	operationContext, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := operator.SearchReplaceWordPress(operationContext, payload.Site, payload.BackupTarget, payload.Change, payload.DryRun, request.IdempotencyKey)
	if encoded, encodeErr := json.Marshal(result); encodeErr == nil {
		response.Result = encoded
	}
	if err != nil {
		response.Error = err.Error()
		return response, true
	}
	response.OK = true
	return response, true
}
