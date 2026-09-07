package broker

import (
	"context"
	"encoding/json"
	"github.com/lum1t4/wpx/internal/model"
	"testing"
)

type fakeHosting struct{ calls int }

func (f *fakeHosting) Provision(context.Context, model.Site) error     { return nil }
func (f *fakeHosting) Disable(context.Context, model.Site, bool) error { return nil }
func (f *fakeHosting) ApplySnippets(context.Context, model.Site, model.SiteSnippets) error {
	return nil
}
func (f *fakeHosting) IssueCertificate(context.Context, model.Site) error { return nil }
func (f *fakeHosting) IssueDNSCertificate(context.Context, model.Site, model.DNSProvider, bool) error {
	return nil
}
func (f *fakeHosting) CreateStaging(context.Context, model.Site, model.Site, string, string) error {
	return nil
}
func (f *fakeHosting) InspectStaging(context.Context, model.Site, model.Site) (StagingInspection, error) {
	return StagingInspection{}, nil
}
func (f *fakeHosting) DeployStaging(context.Context, model.Site, model.Site, model.BackupTarget, StagingSelection, string) (string, error) {
	return "", nil
}
func (f *fakeHosting) ApplyPerformance(context.Context, model.Site) error { return nil }
func (f *fakeHosting) UpdateWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressUpdate, string) (string, error) {
	return "", nil
}
func (f *fakeHosting) ApplyNodeRuntime(context.Context, model.Site, model.NodeRuntime) error {
	f.calls++
	return nil
}
func (f *fakeHosting) ApplyFTPUser(context.Context, model.Site, model.FTPUser) error {
	f.calls++
	return nil
}
func (f *fakeHosting) DeleteFTPUser(context.Context, model.Site, model.FTPUser) error {
	f.calls++
	return nil
}
func (f *fakeHosting) ApplyMailService(context.Context, model.MailService) error {
	f.calls++
	return nil
}
func TestHostingDispatchRejectsTraversal(t *testing.T) {
	site := model.Site{ID: "node-app", Domain: "node.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:3000"}
	payload, _ := json.Marshal(ApplyNodeRuntimeRequest{Site: site, Runtime: model.NodeRuntime{SiteID: site.ID, Entrypoint: "../app.js", Port: 3000, NodeVersion: "24.20.0"}})
	host := &fakeHosting{}
	response, handled := (&Server{Provisioner: host}).dispatchHosting(Request{ID: "r", Operation: OpApplyNodeRuntime, Payload: payload})
	if !handled || response.OK || host.calls != 0 {
		t.Fatalf("handled=%v response=%#v calls=%d", handled, response, host.calls)
	}
}
