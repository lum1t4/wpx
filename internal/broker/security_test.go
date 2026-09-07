package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type testSecurityProvisioner struct {
	SiteProvisioner
	applyCalls   int
	inspectCalls int
}

func (p *testSecurityProvisioner) ApplySecurity(context.Context, model.Site, model.SecuritySettings) error {
	p.applyCalls++
	return nil
}
func (p *testSecurityProvisioner) InspectSecurity(context.Context, model.Site) (SecurityReport, error) {
	p.inspectCalls++
	return SecurityReport{Risk: "high", Sources: []SecuritySource{{IP: "203.0.113.8", Risk: "high"}}}, nil
}
func (p *testSecurityProvisioner) SecurityAvailable() bool               { return true }
func (p *testSecurityProvisioner) InstallSecurity(context.Context) error { return nil }

func TestSecurityDispatchRejectsTraversalAndUnknownFields(t *testing.T) {
	operator := &testSecurityProvisioner{}
	server := &Server{Provisioner: operator}
	badSite := model.Site{ID: "../../etc", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	payload, _ := json.Marshal(SecurityInspectRequest{Site: badSite})
	response, handled := server.dispatchSecurity(Request{ID: "request", Operation: OpSecurityInspect, Payload: payload})
	if !handled || response.OK || operator.inspectCalls != 0 {
		t.Fatalf("response=%#v calls=%d", response, operator.inspectCalls)
	}
	valid := `{"site":{"id":"wp-site","domain":"wp.example.com","kind":"wordpress","php_version":"8.4","status":"active","tls_status":"","created_at":""},"unexpected":true}`
	response, _ = server.dispatchSecurity(Request{ID: "request", Operation: OpSecurityInspect, Payload: json.RawMessage(valid)})
	if response.OK || operator.inspectCalls != 0 {
		t.Fatalf("unknown field reached operator: %#v", response)
	}
}

func TestSecurityDispatchValidatesReturnedAddresses(t *testing.T) {
	operator := &testSecurityProvisioner{}
	operator.SiteProvisioner = nil
	server := &Server{Provisioner: invalidReportSecurityProvisioner{testSecurityProvisioner: operator}}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	payload, _ := json.Marshal(SecurityInspectRequest{Site: site})
	response, _ := server.dispatchSecurity(Request{ID: "request", Operation: OpSecurityInspect, Payload: payload})
	if response.OK {
		t.Fatalf("invalid address crossed broker: %#v", response)
	}
}

func TestSecurityInstallRejectsPayloadFields(t *testing.T) {
	operator := &testSecurityProvisioner{}
	response, handled := (&Server{Provisioner: operator}).dispatchSecurity(Request{ID: "request", Operation: OpSecurityInstall, Payload: json.RawMessage(`{"package":"anything"}`)})
	if !handled || response.OK {
		t.Fatalf("installation accepted caller-selected package: %#v", response)
	}
}

type invalidReportSecurityProvisioner struct{ *testSecurityProvisioner }

func (p invalidReportSecurityProvisioner) InspectSecurity(context.Context, model.Site) (SecurityReport, error) {
	return SecurityReport{Sources: []SecuritySource{{IP: "not an address"}}}, nil
}
