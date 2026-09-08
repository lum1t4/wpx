package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type recordingWordPressDebugManager struct {
	SiteProvisioner
	calls []Operation
}

func (m *recordingWordPressDebugManager) WordPressDebugStatus(context.Context, model.Site) (model.WordPressDebugStatus, error) {
	m.calls = append(m.calls, OpWordPressDebugStatus)
	return model.WordPressDebugStatus{Managed: true}, nil
}
func (m *recordingWordPressDebugManager) SetWordPressDebug(context.Context, model.Site, bool) error {
	m.calls = append(m.calls, OpWordPressDebugSet)
	return nil
}
func (m *recordingWordPressDebugManager) ReadWordPressDebugLog(context.Context, model.Site) (model.WordPressDebugLog, error) {
	m.calls = append(m.calls, OpWordPressDebugRead)
	return model.WordPressDebugLog{Content: "safe"}, nil
}
func (m *recordingWordPressDebugManager) ClearWordPressDebugLog(context.Context, model.Site) error {
	m.calls = append(m.calls, OpWordPressDebugClear)
	return nil
}

func TestWordPressDebugBrokerStrictlyValidatesEveryOperation(t *testing.T) {
	site := model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	for _, operation := range []Operation{OpWordPressDebugStatus, OpWordPressDebugSet, OpWordPressDebugRead, OpWordPressDebugClear} {
		t.Run(string(operation), func(t *testing.T) {
			manager := &recordingWordPressDebugManager{}
			var payload []byte
			if operation == OpWordPressDebugSet {
				enabled := true
				payload, _ = json.Marshal(WordPressDebugSetRequest{Site: site, Enabled: &enabled})
			} else {
				payload, _ = json.Marshal(WordPressDebugRequest{Site: site})
			}
			response, handled := (&Server{Provisioner: manager}).dispatchWordPressDebug(Request{Operation: operation, Payload: payload})
			if !handled || !response.OK || len(manager.calls) != 1 || manager.calls[0] != operation {
				t.Fatalf("valid request = %+v calls=%v", response, manager.calls)
			}
			payload = append(payload[:len(payload)-1], []byte(`,"path":"/etc/passwd"}`)...)
			response, handled = (&Server{Provisioner: manager}).dispatchWordPressDebug(Request{Operation: operation, Payload: payload})
			if !handled || response.OK || len(manager.calls) != 1 {
				t.Fatalf("unknown field crossed boundary: %+v calls=%v", response, manager.calls)
			}
		})
	}
}

func TestWordPressDebugSetRequiresExplicitBoolean(t *testing.T) {
	site := model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	payload, _ := json.Marshal(WordPressDebugSetRequest{Site: site})
	manager := &recordingWordPressDebugManager{}
	response, handled := (&Server{Provisioner: manager}).dispatchWordPressDebug(Request{Operation: OpWordPressDebugSet, Payload: payload})
	if !handled || response.OK || len(manager.calls) != 0 {
		t.Fatalf("missing enabled value accepted: %+v calls=%v", response, manager.calls)
	}
}
