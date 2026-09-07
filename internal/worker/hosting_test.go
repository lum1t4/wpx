package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type hostingWorkerProvisioner struct {
	SiteProvisioner
	nodeCalls []model.NodeRuntime
	nodeKeys  []string
	nodeErr   error
}

func (p *hostingWorkerProvisioner) ApplyNodeRuntime(_ context.Context, _ model.Site, runtime model.NodeRuntime, key string) error {
	p.nodeCalls = append(p.nodeCalls, runtime)
	p.nodeKeys = append(p.nodeKeys, key)
	return p.nodeErr
}

func (*hostingWorkerProvisioner) ApplyFTPUser(context.Context, model.Site, model.FTPUser, string) error {
	return nil
}

func (*hostingWorkerProvisioner) DeleteFTPUser(context.Context, model.Site, model.FTPUser, string) error {
	return nil
}

func (*hostingWorkerProvisioner) ApplyMailService(context.Context, model.MailService, string) error {
	return nil
}

func hostingWorkerFixture(t *testing.T) (*store.Store, store.User, *Worker, model.Site) {
	t.Helper()
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	owner, err := state.CreateOwner(ctx, "hosting-operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "node-worker", Domain: "node-worker.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:3100"}
	if _, err := state.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision site: processed=%v error=%v", processed, err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil || site.Status != "active" {
		t.Fatalf("active site=%#v error=%v", site, err)
	}
	return state, owner, w, site
}

func TestWorkerAppliesPersistedNodeRuntime(t *testing.T) {
	ctx := context.Background()
	state, owner, w, site := hostingWorkerFixture(t)
	runtime := model.NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Arguments: []string{"--production"}, Port: 3100, NodeVersion: "24.20.0"}
	jobID, err := state.ConfigureNodeRuntime(ctx, owner, site, runtime)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &hostingWorkerProvisioner{SiteProvisioner: &fakeProvisioner{}}
	w.Provisioner = provisioner
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("apply runtime: processed=%v error=%v", processed, err)
	}
	if len(provisioner.nodeCalls) != 1 || provisioner.nodeCalls[0].Entrypoint != runtime.Entrypoint || provisioner.nodeCalls[0].Port != runtime.Port || len(provisioner.nodeCalls[0].Arguments) != 1 || provisioner.nodeCalls[0].Arguments[0] != "--production" || provisioner.nodeKeys[0] != queued.IdempotencyKey {
		t.Fatalf("persisted runtime was not dispatched: calls=%#v keys=%#v", provisioner.nodeCalls, provisioner.nodeKeys)
	}
	job, err := state.Job(ctx, jobID)
	stored, runtimeErr := state.NodeRuntime(ctx, site.ID)
	if err != nil || runtimeErr != nil || job.Status != "succeeded" || stored.Status != "active" {
		t.Fatalf("job=%#v runtime=%#v jobError=%v runtimeError=%v", job, stored, err, runtimeErr)
	}
}

func TestWorkerRequeuesUncertainNodeRuntimeWithSameKey(t *testing.T) {
	ctx := context.Background()
	state, owner, w, site := hostingWorkerFixture(t)
	jobID, err := state.ConfigureNodeRuntime(ctx, owner, site, model.NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Port: 3100, NodeVersion: "24.20.0"})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &hostingWorkerProvisioner{SiteProvisioner: &fakeProvisioner{}, nodeErr: errors.Join(broker.ErrOutcomeUnknown, errors.New("lost broker response"))}
	w.Provisioner = provisioner
	if processed, err := w.ProcessOne(ctx); err == nil || processed {
		t.Fatalf("uncertain apply: processed=%v error=%v", processed, err)
	}
	requeued, err := state.Job(ctx, jobID)
	stored, runtimeErr := state.NodeRuntime(ctx, site.ID)
	if err != nil || runtimeErr != nil || requeued.Status != "queued" || requeued.IdempotencyKey != queued.IdempotencyKey || stored.Status != "queued" {
		t.Fatalf("job=%#v runtime=%#v jobError=%v runtimeError=%v", requeued, stored, err, runtimeErr)
	}
	provisioner.nodeErr = nil
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("retry runtime: processed=%v error=%v", processed, err)
	}
	if len(provisioner.nodeKeys) != 2 || provisioner.nodeKeys[0] != queued.IdempotencyKey || provisioner.nodeKeys[1] != queued.IdempotencyKey {
		t.Fatalf("retry changed broker key: %#v", provisioner.nodeKeys)
	}
	completed, err := state.Job(ctx, jobID)
	stored, runtimeErr = state.NodeRuntime(ctx, site.ID)
	if err != nil || runtimeErr != nil || completed.Status != "succeeded" || stored.Status != "active" {
		t.Fatalf("retried job=%#v runtime=%#v jobError=%v runtimeError=%v", completed, stored, err, runtimeErr)
	}
}

func TestWorkerEnablesOrdinarySiteWithoutHostingProvisioner(t *testing.T) {
	ctx := context.Background()
	state, owner, w, _ := hostingWorkerFixture(t)
	if _, err := state.EnqueueSiteDisable(ctx, owner, "node-worker"); err != nil {
		t.Fatal(err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("disable site: processed=%v error=%v", processed, err)
	}
	if _, err := state.EnqueueSiteEnable(ctx, owner, "node-worker"); err != nil {
		t.Fatal(err)
	}
	// fakeProvisioner deliberately lacks hostingProvisioner. A site with no
	// Node runtime or FTP users must retain the original lifecycle contract.
	w.Provisioner = &fakeProvisioner{}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("enable ordinary site: processed=%v error=%v", processed, err)
	}
	site, err := state.Site(ctx, "node-worker")
	if err != nil || site.Status != "active" {
		t.Fatalf("enabled site=%#v error=%v", site, err)
	}
}
