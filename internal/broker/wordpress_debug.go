package broker

import (
	"context"
	"encoding/json"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	OpWordPressDebugStatus Operation = "wordpress.debug_status"
	OpWordPressDebugSet    Operation = "wordpress.debug_set"
	OpWordPressDebugRead   Operation = "wordpress.debug_read"
	OpWordPressDebugClear  Operation = "wordpress.debug_clear"
)

type WordPressDebugRequest struct {
	Site model.Site `json:"site"`
}

type WordPressDebugSetRequest struct {
	Site    model.Site `json:"site"`
	Enabled *bool      `json:"enabled"`
}

type wordpressDebugManager interface {
	WordPressDebugStatus(context.Context, model.Site) (model.WordPressDebugStatus, error)
	SetWordPressDebug(context.Context, model.Site, bool) error
	ReadWordPressDebugLog(context.Context, model.Site) (model.WordPressDebugLog, error)
	ClearWordPressDebugLog(context.Context, model.Site) error
}

func (s *Server) dispatchWordPressDebug(request Request) (Response, bool) {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	if request.Operation != OpWordPressDebugStatus && request.Operation != OpWordPressDebugSet && request.Operation != OpWordPressDebugRead && request.Operation != OpWordPressDebugClear {
		return response, false
	}
	manager, ok := s.Provisioner.(wordpressDebugManager)
	if !ok {
		response.Error = "WordPress debug operations are unavailable"
		return response, true
	}
	var site model.Site
	if request.Operation == OpWordPressDebugSet {
		var payload WordPressDebugSetRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || payload.Enabled == nil {
			response.Error = "invalid WordPress debug request"
			return response, true
		}
		site = payload.Site
		if model.ValidateWordPressDebugSite(site) != nil {
			response.Error = "invalid WordPress debug request"
			return response, true
		}
		if err := manager.SetWordPressDebug(context.Background(), site, *payload.Enabled); err != nil {
			response.Error = "could not update WordPress debug settings"
			return response, true
		}
		response.Result = json.RawMessage(`{"updated":true}`)
	} else {
		var payload WordPressDebugRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateWordPressDebugSite(payload.Site) != nil {
			response.Error = "invalid WordPress debug request"
			return response, true
		}
		site = payload.Site
		switch request.Operation {
		case OpWordPressDebugStatus:
			result, err := manager.WordPressDebugStatus(context.Background(), site)
			if err != nil {
				response.Error = "could not inspect WordPress debug settings"
				return response, true
			}
			response.Result, _ = json.Marshal(result)
		case OpWordPressDebugRead:
			result, err := manager.ReadWordPressDebugLog(context.Background(), site)
			if err != nil {
				response.Error = "could not read the WordPress debug log"
				return response, true
			}
			response.Result, _ = json.Marshal(result)
		case OpWordPressDebugClear:
			if err := manager.ClearWordPressDebugLog(context.Background(), site); err != nil {
				response.Error = "could not clear the WordPress debug log"
				return response, true
			}
			response.Result = json.RawMessage(`{"cleared":true}`)
		}
	}
	response.OK = true
	return response, true
}
