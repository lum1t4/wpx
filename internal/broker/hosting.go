package broker

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	OpApplyNodeRuntime Operation = "hosting.node_apply"
	OpApplyFTPUser     Operation = "hosting.ftp_apply"
	OpDeleteFTPUser    Operation = "hosting.ftp_delete"
	OpApplyMailService Operation = "hosting.mail_apply"
)

type HostingOperator interface {
	ApplyNodeRuntime(context.Context, model.Site, model.NodeRuntime) error
	ApplyFTPUser(context.Context, model.Site, model.FTPUser) error
	DeleteFTPUser(context.Context, model.Site, model.FTPUser) error
	ApplyMailService(context.Context, model.MailService) error
}

type ApplyNodeRuntimeRequest struct {
	Site    model.Site        `json:"site"`
	Runtime model.NodeRuntime `json:"runtime"`
}
type ApplyFTPUserRequest struct {
	Site model.Site    `json:"site"`
	User model.FTPUser `json:"user"`
}
type ApplyMailServiceRequest struct {
	Service model.MailService `json:"service"`
}

// dispatchHosting is an extension dispatch point. Central dispatch calls it
// before returning an unknown-operation response.
func (s *Server) dispatchHosting(request Request) (Response, bool) {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	operator, ok := s.Provisioner.(HostingOperator)
	switch request.Operation {
	case OpApplyNodeRuntime:
		var payload ApplyNodeRuntimeRequest
		if decodeHosting(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateNodeRuntime(payload.Site, payload.Runtime) != nil {
			response.Error = "invalid Node runtime request"
			return response, true
		}
		if !ok {
			response.Error = "hosting operations are unavailable"
			return response, true
		}
		if err := operator.ApplyNodeRuntime(context.Background(), payload.Site, payload.Runtime); err != nil {
			response.Error = "apply Node runtime: " + err.Error()
			return response, true
		}
	case OpApplyFTPUser:
		var payload ApplyFTPUserRequest
		if decodeHosting(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateFTPUser(payload.User) != nil || payload.User.SiteID != payload.Site.ID {
			response.Error = "invalid FTP user request"
			return response, true
		}
		if !ok {
			response.Error = "hosting operations are unavailable"
			return response, true
		}
		if err := operator.ApplyFTPUser(context.Background(), payload.Site, payload.User); err != nil {
			response.Error = "apply FTP user: " + err.Error()
			return response, true
		}
	case OpApplyMailService:
		var payload ApplyMailServiceRequest
		if decodeHosting(request.Payload, &payload) != nil || model.ValidateMailService(payload.Service) != nil || !payload.Service.Enabled {
			response.Error = "invalid mail service request"
			return response, true
		}
		if !ok {
			response.Error = "hosting operations are unavailable"
			return response, true
		}
		if err := operator.ApplyMailService(context.Background(), payload.Service); err != nil {
			response.Error = "apply mail service: " + err.Error()
			return response, true
		}
	case OpDeleteFTPUser:
		var payload ApplyFTPUserRequest
		if decodeHosting(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateFTPUser(payload.User) != nil || payload.User.SiteID != payload.Site.ID {
			response.Error = "invalid FTP user deletion request"
			return response, true
		}
		if !ok {
			response.Error = "hosting operations are unavailable"
			return response, true
		}
		if err := operator.DeleteFTPUser(context.Background(), payload.Site, payload.User); err != nil {
			response.Error = "delete FTP user: " + err.Error()
			return response, true
		}
	default:
		return response, false
	}
	response.OK = true
	response.Result = json.RawMessage(`{"applied":true}`)
	return response, true
}
func decodeHosting(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
