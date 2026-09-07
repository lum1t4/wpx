package broker

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

const (
	OpOperatorDiagnostics Operation = "operator.diagnostics"
	OpOperatorEnsureSwap  Operation = "operator.ensure_swap"
)

type OperatorDiagnosticsRequest struct {
	Sites        []model.Site        `json:"sites"`
	NodeRuntimes []model.NodeRuntime `json:"node_runtimes,omitempty"`
}
type OperatorEnsureSwapRequest struct {
	SizeMiB int `json:"size_mib"`
}
type OperatorServiceStatus struct {
	Name        string   `json:"name"`
	Active      string   `json:"active"`
	Sub         string   `json:"sub"`
	Restarts    int      `json:"restarts"`
	ExitStatus  int      `json:"exit_status"`
	Required    bool     `json:"required"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}
type OperatorCertificateStatus struct {
	SiteID   string `json:"site_id"`
	Domain   string `json:"domain"`
	NotAfter string `json:"not_after,omitempty"`
	Error    string `json:"error,omitempty"`
}
type OperatorDiagnosticsResult struct {
	Services     []OperatorServiceStatus     `json:"services"`
	Certificates []OperatorCertificateStatus `json:"certificates"`
	OOMEvents    []string                    `json:"oom_events"`
}
type OperatorEnsureSwapResult struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

type operatorDiagnostics interface {
	OperatorDiagnostics(context.Context, []model.Site, []model.NodeRuntime) (OperatorDiagnosticsResult, error)
	EnsureOperatorSwap(context.Context, int) (string, bool, error)
}

// dispatchOperatorAlerts is kept outside the central switch so the privileged
// surface can be reviewed and tested as one feature. Unknown operations fall
// through to the original dispatcher.
func (s *Server) dispatchOperatorAlerts(request Request) (Response, bool) {
	if request.Operation != OpOperatorDiagnostics && request.Operation != OpOperatorEnsureSwap {
		return Response{}, false
	}
	response := Response{Version: ProtocolVersion, ID: request.ID}
	operator, ok := s.Provisioner.(operatorDiagnostics)
	if !ok {
		response.Error = "operator diagnostics are unavailable"
		return response, true
	}
	switch request.Operation {
	case OpOperatorDiagnostics:
		var payload OperatorDiagnosticsRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || len(payload.Sites) > 1000 || len(payload.NodeRuntimes) > len(payload.Sites) {
			response.Error = "invalid operator diagnostics request"
			return response, true
		}
		seen := make(map[string]model.Site, len(payload.Sites))
		for _, site := range payload.Sites {
			if _, duplicate := seen[site.ID]; model.ValidateSite(site) != nil || duplicate || strings.TrimSpace(site.Domain) != site.Domain {
				response.Error = "invalid operator diagnostics request"
				return response, true
			}
			seen[site.ID] = site
		}
		for _, runtime := range payload.NodeRuntimes {
			site, exists := seen[runtime.SiteID]
			if !exists || model.ValidateNodeRuntime(site, runtime) != nil {
				response.Error = "invalid operator diagnostics request"
				return response, true
			}
		}
		result, err := operator.OperatorDiagnostics(context.Background(), payload.Sites, payload.NodeRuntimes)
		if err != nil {
			response.Error = "collect operator diagnostics: " + err.Error()
			return response, true
		}
		response.OK = true
		response.Result, _ = json.Marshal(result)
	case OpOperatorEnsureSwap:
		var payload OperatorEnsureSwapRequest
		if decodeLifecyclePayload(request.Payload, &payload) != nil || payload.SizeMiB < 512 || payload.SizeMiB > 8192 || payload.SizeMiB%512 != 0 {
			response.Error = "invalid managed swap request"
			return response, true
		}
		path, created, err := operator.EnsureOperatorSwap(context.Background(), payload.SizeMiB)
		if err != nil {
			response.Error = "ensure managed swap: " + err.Error()
			return response, true
		}
		response.OK = true
		response.Result, _ = json.Marshal(OperatorEnsureSwapResult{Path: path, Created: created})
	}
	return response, true
}
