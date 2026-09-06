package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type lifecycleManager struct {
	SiteProvisioner
	domainCalls int
	deleteCalls int
	key         string
	stopPHP     bool
	err         error
}

func (m *lifecycleManager) ChangeDomain(_ context.Context, _ model.Site, _ model.DomainChange, key string) error {
	m.domainCalls++
	m.key = key
	return m.err
}

func (m *lifecycleManager) DeleteSite(_ context.Context, _ model.Site, stopPHP bool, key string) error {
	m.deleteCalls++
	m.key, m.stopPHP = key, stopPHP
	return m.err
}

func domainRequest(t *testing.T) ChangeDomainRequest {
	t.Helper()
	return ChangeDomainRequest{
		Site:   model.Site{ID: "94712940-f3cb-4545-80db-8f850a305514", Domain: "old.example.com", Kind: model.Static, Status: "domain_changing", TLSStatus: "none"},
		Change: model.DomainChange{PreviousDomain: "old.example.com", Domain: "new.example.com", PreviousTLSStatus: "none"},
	}
}

func lifecycleRequest(t *testing.T, operation Operation, payload any) Request {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Version: ProtocolVersion, ID: "request", Operation: operation, IdempotencyKey: "durable-site-job", Payload: raw}
}

func TestDomainBrokerValidatesBeforePrivilegedCall(t *testing.T) {
	for _, scenario := range []string{
		"valid", "traversal", "invalid site", "invalid domain", "wrong previous domain", "unchanged domain", "wrong status", "unknown field", "unknown nested field", "trailing JSON", "empty key", "blank key", "long key",
	} {
		t.Run(scenario, func(t *testing.T) {
			manager := &lifecycleManager{}
			server := &Server{Provisioner: manager}
			payload := domainRequest(t)
			switch scenario {
			case "traversal":
				payload.Site.ID = "../../outside"
			case "invalid site":
				payload.Site.Kind = "invalid"
			case "invalid domain":
				payload.Change.Domain = "example.com;command"
			case "wrong previous domain":
				payload.Change.PreviousDomain = "another.example.com"
			case "unchanged domain":
				payload.Change.Domain = payload.Site.Domain
			case "wrong status":
				payload.Site.Status = "active"
			}
			request := lifecycleRequest(t, OpChangeDomain, payload)
			switch scenario {
			case "unknown field":
				request.Payload = append(request.Payload[:len(request.Payload)-1], []byte(`,"command":"unsafe"}`)...)
			case "unknown nested field":
				request.Payload = json.RawMessage(strings.Replace(string(request.Payload), `"change":{`, `"change":{"command":"unsafe",`, 1))
			case "trailing JSON":
				request.Payload = append(request.Payload, []byte(` {"command":"unsafe"}`)...)
			case "empty key":
				request.IdempotencyKey = ""
			case "blank key":
				request.IdempotencyKey = " \t\n"
			case "long key":
				request.IdempotencyKey = strings.Repeat("x", 161)
			}
			response := server.dispatch(request)
			if scenario == "valid" {
				if !response.OK || manager.domainCalls != 1 || manager.key != request.IdempotencyKey {
					t.Fatalf("valid request rejected or key not forwarded: %+v; manager=%+v", response, manager)
				}
			} else if response.OK || manager.domainCalls != 0 {
				t.Fatalf("invalid request crossed the root boundary: %+v; calls=%d", response, manager.domainCalls)
			}
		})
	}
}

func TestDeleteBrokerValidatesBeforePrivilegedCall(t *testing.T) {
	for _, scenario := range []string{
		"deleting", "delete_failed", "stop PHP", "traversal", "invalid site", "wrong status", "unknown field", "unknown nested field", "trailing JSON", "empty key", "blank key", "long key", "maximum key",
	} {
		t.Run(scenario, func(t *testing.T) {
			manager := &lifecycleManager{}
			server := &Server{Provisioner: manager}
			payload := DeleteSiteRequest{Site: domainRequest(t).Site}
			payload.Site.Status = "deleting"
			switch scenario {
			case "delete_failed":
				payload.Site.Status = "delete_failed"
			case "stop PHP":
				payload.StopPHP = true
				payload.Site.Kind, payload.Site.PHPVersion = model.PHP, "8.4"
			case "traversal":
				payload.Site.ID = "../../outside"
			case "invalid site":
				payload.Site.Kind = "invalid"
			case "wrong status":
				payload.Site.Status = "active"
			}
			request := lifecycleRequest(t, OpDeleteSite, payload)
			switch scenario {
			case "unknown field":
				request.Payload = append(request.Payload[:len(request.Payload)-1], []byte(`,"command":"unsafe"}`)...)
			case "unknown nested field":
				request.Payload = json.RawMessage(strings.Replace(string(request.Payload), `"site":{`, `"site":{"command":"unsafe",`, 1))
			case "trailing JSON":
				request.Payload = append(request.Payload, []byte(` null`)...)
			case "empty key":
				request.IdempotencyKey = ""
			case "blank key":
				request.IdempotencyKey = " \t\n"
			case "long key":
				request.IdempotencyKey = strings.Repeat("x", 161)
			case "maximum key":
				request.IdempotencyKey = strings.Repeat("x", 160)
			}
			response := server.dispatch(request)
			valid := scenario == "deleting" || scenario == "delete_failed" || scenario == "stop PHP" || scenario == "maximum key"
			if valid {
				if !response.OK || manager.deleteCalls != 1 || manager.key != request.IdempotencyKey || manager.stopPHP != payload.StopPHP {
					t.Fatalf("valid request rejected or arguments lost: %+v; manager=%+v", response, manager)
				}
			} else if response.OK || manager.deleteCalls != 0 {
				t.Fatalf("invalid request crossed the root boundary: %+v; calls=%d", response, manager.deleteCalls)
			}
		})
	}
}

func TestLifecycleBrokerUnavailableAndHostErrors(t *testing.T) {
	for _, operation := range []Operation{OpChangeDomain, OpDeleteSite} {
		t.Run(string(operation), func(t *testing.T) {
			var payload any = domainRequest(t)
			if operation == OpDeleteSite {
				site := domainRequest(t).Site
				site.Status = "deleting"
				payload = DeleteSiteRequest{Site: site}
			}
			request := lifecycleRequest(t, operation, payload)
			for _, provisioner := range []SiteProvisioner{nil, &phpVersionManager{}} {
				response := (&Server{Provisioner: provisioner}).dispatch(request)
				if response.OK || !strings.Contains(response.Error, "unavailable") {
					t.Fatalf("missing implementation must fail explicitly: %+v", response)
				}
			}
			manager := &lifecycleManager{err: errors.New("host failure")}
			response := (&Server{Provisioner: manager}).dispatch(request)
			if response.OK || !strings.Contains(response.Error, "host failure") || len(response.Result) != 0 {
				t.Fatalf("host failure was lost or falsely claimed recovery: %+v", response)
			}
		})
	}
}

func TestDomainBrokerPreservesExplicitRecovery(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) {
			manager := &lifecycleManager{err: fmt.Errorf("wrapped: %w", &model.DomainChangeError{Err: errors.New("host failure"), PreviousRestored: restored})}
			response := (&Server{Provisioner: manager}).dispatch(lifecycleRequest(t, OpChangeDomain, domainRequest(t)))
			var result ChangeDomainResult
			if response.OK || len(response.Result) == 0 || json.Unmarshal(response.Result, &result) != nil || result.PreviousRestored != restored {
				t.Fatalf("explicit recovery was lost: %+v", response)
			}
		})
	}
}
