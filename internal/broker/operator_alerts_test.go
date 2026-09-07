package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type fakeOperatorDiagnostics struct {
	SiteProvisioner
	diagnostics int
	swaps       int
}

func (f *fakeOperatorDiagnostics) OperatorDiagnostics(context.Context, []model.Site, []model.NodeRuntime) (OperatorDiagnosticsResult, error) {
	f.diagnostics++
	return OperatorDiagnosticsResult{}, nil
}
func (f *fakeOperatorDiagnostics) EnsureOperatorSwap(context.Context, int) (string, bool, error) {
	f.swaps++
	return "/var/lib/wpx/swap/wpx.swap", true, nil
}

func TestOperatorAlertBrokerValidatesEntireBoundary(t *testing.T) {
	operator := &fakeOperatorDiagnostics{}
	server := &Server{Provisioner: operator}
	request := func(op Operation, payload any) Response {
		raw, _ := json.Marshal(payload)
		response, handled := server.dispatchOperatorAlerts(Request{Operation: op, Payload: raw, IdempotencyKey: "test"})
		if !handled {
			t.Fatal("operation not handled")
		}
		return response
	}
	if response := request(OpOperatorEnsureSwap, OperatorEnsureSwapRequest{SizeMiB: 513}); response.OK || operator.swaps != 0 {
		t.Fatal("invalid swap request crossed boundary")
	}
	if response := request(OpOperatorEnsureSwap, OperatorEnsureSwapRequest{SizeMiB: 2048}); !response.OK || operator.swaps != 1 {
		t.Fatalf("valid swap rejected: %#v", response)
	}
	invalid := model.Site{ID: "../../etc", Domain: "example.com", Kind: model.Static}
	if response := request(OpOperatorDiagnostics, OperatorDiagnosticsRequest{Sites: []model.Site{invalid}}); response.OK || operator.diagnostics != 0 {
		t.Fatal("invalid site crossed diagnostics boundary")
	}
	valid := model.Site{ID: "valid-site", Domain: "example.com", Kind: model.Static, Status: "active", TLSStatus: "active"}
	if response := request(OpOperatorDiagnostics, OperatorDiagnosticsRequest{Sites: []model.Site{valid, valid}}); response.OK || operator.diagnostics != 0 {
		t.Fatal("duplicate site crossed diagnostics boundary")
	}
	if response := request(OpOperatorDiagnostics, OperatorDiagnosticsRequest{Sites: []model.Site{valid}}); !response.OK || operator.diagnostics != 1 {
		t.Fatalf("valid diagnostics rejected: %#v", response)
	}
}
