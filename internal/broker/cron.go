package broker

import (
	"context"
	"encoding/json"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	OpCronApply          Operation = "site.cron_apply"
	OpWordPressCronApply Operation = "site.wordpress_cron_apply"
)

type ApplyCronRequest struct {
	Site     model.Site         `json:"site"`
	Schedule model.CronSchedule `json:"schedule"`
	Delete   bool               `json:"delete"`
}

type ApplyWordPressCronRequest struct {
	Site    model.Site                 `json:"site"`
	Setting model.WordPressCronSetting `json:"setting"`
}

type cronManager interface {
	ApplyCronSchedule(context.Context, model.Site, model.CronSchedule, bool) error
	ApplyWordPressCronReplacement(context.Context, model.Site, model.WordPressCronSetting) error
}

// dispatchCron owns the complete validation boundary for cron operations. The
// central dispatcher calls it before its existing switch and uses the boolean
// to distinguish an unrelated operation from a handled rejection.
func (s *Server) dispatchCron(request Request) (Response, bool) {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	if request.Operation != OpCronApply && request.Operation != OpWordPressCronApply {
		return response, false
	}
	manager, ok := s.Provisioner.(cronManager)
	if !ok {
		response.Error = "cron operations are unavailable"
		return response, true
	}
	switch request.Operation {
	case OpCronApply:
		var payload ApplyCronRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateCronSchedule(payload.Schedule) != nil || payload.Schedule.SiteID != payload.Site.ID || (payload.Site.Status != "active" && payload.Site.Status != "disabled") {
			response.Error = "invalid cron schedule request"
			return response, true
		}
		if err := manager.ApplyCronSchedule(context.Background(), payload.Site, payload.Schedule, payload.Delete); err != nil {
			response.Error = "apply cron schedule: " + err.Error()
			return response, true
		}
	case OpWordPressCronApply:
		var payload ApplyWordPressCronRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateWordPressCronSetting(payload.Setting) != nil || payload.Setting.SiteID != payload.Site.ID || payload.Site.Kind != model.WordPress || (payload.Site.Status != "active" && payload.Site.Status != "disabled") {
			response.Error = "invalid WordPress cron request"
			return response, true
		}
		if err := manager.ApplyWordPressCronReplacement(context.Background(), payload.Site, payload.Setting); err != nil {
			response.Error = "apply WordPress cron replacement: " + err.Error()
			return response, true
		}
	}
	response.OK = true
	response.Result = json.RawMessage(`{"applied":true}`)
	return response, true
}
