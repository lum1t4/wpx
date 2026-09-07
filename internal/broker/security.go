package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	OpSecurityInspect Operation = "wordpress.security_inspect"
	OpSecurityApply   Operation = "wordpress.security_apply"
	OpSecurityStatus  Operation = "wordpress.security_status"
	OpSecurityInstall Operation = "wordpress.security_install"
)

type SecurityInspectRequest struct {
	Site model.Site `json:"site"`
}

type SecurityApplyRequest struct {
	Site     model.Site             `json:"site"`
	Settings model.SecuritySettings `json:"settings"`
}

type SecurityApplyResult struct {
	Active bool `json:"active"`
}

type SecurityStatusResult struct {
	Available bool `json:"available"`
}

type SecuritySource struct {
	IP             string   `json:"ip"`
	Risk           string   `json:"risk"`
	Score          int      `json:"score"`
	Requests       int      `json:"requests"`
	LoginAttempts  int      `json:"login_attempts"`
	XMLRPCAttempts int      `json:"xmlrpc_attempts"`
	SensitiveScans int      `json:"sensitive_scans"`
	NotFound       int      `json:"not_found"`
	Reasons        []string `json:"reasons"`
	LastSeen       string   `json:"last_seen"`
}

type SecurityReport struct {
	Risk           string           `json:"risk"`
	LinesRead      int              `json:"lines_read"`
	MalformedLines int              `json:"malformed_lines"`
	Truncated      bool             `json:"truncated"`
	Sources        []SecuritySource `json:"sources"`
}

type SecurityOperator interface {
	InspectSecurity(context.Context, model.Site) (SecurityReport, error)
	ApplySecurity(context.Context, model.Site, model.SecuritySettings) error
	SecurityAvailable() bool
	InstallSecurity(context.Context) error
}

// dispatchSecurity is called by the central dispatch before its switch. It
// keeps feature protocol types and validation out of the shared server file.
func (s *Server) dispatchSecurity(request Request) (Response, bool) {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	if request.Operation != OpSecurityInspect && request.Operation != OpSecurityApply && request.Operation != OpSecurityStatus && request.Operation != OpSecurityInstall {
		return response, false
	}
	operator, ok := s.Provisioner.(SecurityOperator)
	if !ok {
		response.Error = "security operations are unavailable"
		return response, true
	}
	switch request.Operation {
	case OpSecurityStatus:
		if string(request.Payload) != "{}" && string(request.Payload) != "null" {
			response.Error = "invalid security status request"
			return response, true
		}
		response.OK = true
		response.Result, _ = json.Marshal(SecurityStatusResult{Available: operator.SecurityAvailable()})
	case OpSecurityInstall:
		if string(request.Payload) != "{}" && string(request.Payload) != "null" {
			response.Error = "invalid security installation request"
			return response, true
		}
		if err := operator.InstallSecurity(context.Background()); err != nil {
			response.Error = "install security dependencies: " + err.Error()
			return response, true
		}
		response.OK = true
		response.Result, _ = json.Marshal(SecurityStatusResult{Available: true})
	case OpSecurityInspect:
		var payload SecurityInspectRequest
		if !decodeSecurityPayload(request.Payload, &payload) || model.ValidateSite(payload.Site) != nil || payload.Site.Kind != model.WordPress || (payload.Site.Status != "active" && payload.Site.Status != "disabled") {
			response.Error = "invalid security inspection request"
			return response, true
		}
		report, err := operator.InspectSecurity(context.Background(), payload.Site)
		if err != nil {
			response.Error = "inspect site security: " + err.Error()
			return response, true
		}
		for _, source := range report.Sources {
			if _, err := netip.ParseAddr(source.IP); err != nil {
				response.Error = "security inspection returned an invalid address"
				return response, true
			}
		}
		response.OK = true
		response.Result, _ = json.Marshal(report)
	case OpSecurityApply:
		var payload SecurityApplyRequest
		if !decodeSecurityPayload(request.Payload, &payload) || model.ValidateSecuritySettings(payload.Site, payload.Settings) != nil {
			response.Error = "invalid security settings request"
			return response, true
		}
		if err := operator.ApplySecurity(context.Background(), payload.Site, payload.Settings); err != nil {
			response.Error = "apply site security: " + err.Error()
			return response, true
		}
		response.OK = true
		response.Result, _ = json.Marshal(SecurityApplyResult{Active: payload.Settings.Enabled})
	}
	return response, true
}

func decodeSecurityPayload(raw json.RawMessage, destination any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}
