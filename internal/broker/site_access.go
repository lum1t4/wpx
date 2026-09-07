package broker

import (
	"context"
	"encoding/json"

	"github.com/lum1t4/wpx/internal/model"
)

const OpApplySiteAccess Operation = "site.access_apply"

type ApplySiteAccessRequest struct {
	Site     model.Site               `json:"site"`
	Settings model.SiteAccessSettings `json:"settings"`
}

type siteAccessManager interface {
	ApplySiteAccess(context.Context, model.Site, model.SiteAccessSettings) error
}

// dispatchSiteAccess is an optional protocol extension. The central dispatcher
// calls it before its built-in switch, preserving the shared protocol file.
func (s *Server) dispatchSiteAccess(request Request) (Response, bool) {
	if request.Operation != OpApplySiteAccess {
		return Response{}, false
	}
	response := Response{Version: ProtocolVersion, ID: request.ID}
	var payload ApplySiteAccessRequest
	if decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || payload.Site.Status != "active" || payload.Settings.SiteID != payload.Site.ID || model.ValidateSiteAccessSettings(payload.Settings) != nil {
		response.Error = "invalid site access request"
		return response, true
	}
	manager, ok := s.Provisioner.(siteAccessManager)
	if !ok {
		response.Error = "site access management is unavailable"
		return response, true
	}
	if err := manager.ApplySiteAccess(context.Background(), payload.Site, payload.Settings); err != nil {
		response.Error = "apply site access: " + err.Error()
		return response, true
	}
	response.OK = true
	response.Result = json.RawMessage(`{"applied":true}`)
	return response, true
}
