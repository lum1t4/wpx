package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

type ChangeDomainRequest struct {
	Site   model.Site         `json:"site"`
	Change model.DomainChange `json:"change"`
}

// PreviousRestored is an explicit host assertion, not an inference from a
// failed command. The worker may release its site reservation only after it
// knows which domain the host is serving.
type ChangeDomainResult struct {
	PreviousRestored bool `json:"previous_restored"`
}

type DeleteSiteRequest struct {
	Site    model.Site `json:"site"`
	StopPHP bool       `json:"stop_php"`
}

type domainChanger interface {
	ChangeDomain(context.Context, model.Site, model.DomainChange, string) error
}

type siteDeleter interface {
	DeleteSite(context.Context, model.Site, bool, string) error
}

func (s *Server) changeDomain(request Request, response Response) Response {
	var payload ChangeDomainRequest
	if !validLifecycleKey(request.IdempotencyKey) || decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || model.ValidateDomainChange(payload.Site, payload.Change) != nil || payload.Site.Status != "domain_changing" {
		response.Error = "invalid site domain change request"
		return response
	}
	manager, ok := s.Provisioner.(domainChanger)
	if !ok {
		response.Error = "site domain changes are unavailable"
		return response
	}
	if err := manager.ChangeDomain(context.Background(), payload.Site, payload.Change, request.IdempotencyKey); err != nil {
		response.Error = "change site domain: " + err.Error()
		var changeErr *model.DomainChangeError
		if errors.As(err, &changeErr) {
			response.Result, _ = json.Marshal(ChangeDomainResult{PreviousRestored: changeErr.PreviousRestored})
		}
		return response
	}
	response.OK = true
	response.Result = json.RawMessage(`{"changed":true}`)
	return response
}

func (s *Server) deleteSite(request Request, response Response) Response {
	var payload DeleteSiteRequest
	if !validLifecycleKey(request.IdempotencyKey) || decodeLifecyclePayload(request.Payload, &payload) != nil || model.ValidateSite(payload.Site) != nil || (payload.Site.Status != "deleting" && payload.Site.Status != "delete_failed") {
		response.Error = "invalid site deletion request"
		return response
	}
	manager, ok := s.Provisioner.(siteDeleter)
	if !ok {
		response.Error = "site deletion is unavailable"
		return response
	}
	if err := manager.DeleteSite(context.Background(), payload.Site, payload.StopPHP, request.IdempotencyKey); err != nil {
		response.Error = "delete site: " + err.Error()
		return response
	}
	response.OK = true
	response.Result = json.RawMessage(`{"deleted":true}`)
	return response
}

// Lifecycle operations keep a host-side journal keyed by the durable job. The
// key is opaque (never a path); its bound keeps journal names and requests small.
func validLifecycleKey(key string) bool {
	return strings.TrimSpace(key) != "" && len(key) <= 160
}

func decodeLifecyclePayload(raw json.RawMessage, payload any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("payload must contain exactly one JSON object")
	}
	return nil
}

func decodeDomainRecovery(raw json.RawMessage) (bool, error) {
	// A missing boolean is not proof that rollback failed, and certainly is not
	// proof that it succeeded. Preserve the reservation on malformed responses.
	var recovery struct {
		PreviousRestored *bool `json:"previous_restored"`
	}
	if err := decodeLifecyclePayload(raw, &recovery); err != nil {
		return false, err
	}
	if recovery.PreviousRestored == nil {
		return false, errors.New("missing previous_restored recovery field")
	}
	return *recovery.PreviousRestored, nil
}
