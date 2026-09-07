package worker

import (
	"context"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

func (w *Worker) enableSiteHosting(ctx context.Context, site model.Site, key string) error {
	runtime, err := w.Store.NodeRuntime(ctx, site.ID)
	hasRuntime := err == nil
	if err != nil && !errors.Is(err, store.ErrNodeRuntimeNotFound) {
		return err
	}
	users, err := w.Store.FTPUsersForProvision(ctx, site.ID)
	if err != nil {
		return err
	}
	if !hasRuntime && len(users) == 0 {
		return nil
	}
	operator, ok := w.Provisioner.(hostingProvisioner)
	if !ok {
		return errors.New("hosting operations are unavailable")
	}
	if hasRuntime {
		if err := operator.ApplyNodeRuntime(ctx, site, runtime, key+":node"); err != nil {
			return err
		}
	}
	for _, user := range users {
		if err := operator.ApplyFTPUser(ctx, site, user, key+":"+user.ID); err != nil {
			return err
		}
	}
	return nil
}

type hostingProvisioner interface {
	ApplyNodeRuntime(context.Context, model.Site, model.NodeRuntime, string) error
	ApplyFTPUser(context.Context, model.Site, model.FTPUser, string) error
	DeleteFTPUser(context.Context, model.Site, model.FTPUser, string) error
	ApplyMailService(context.Context, model.MailService, string) error
}

func (p BrokerProvisioner) ApplyNodeRuntime(ctx context.Context, site model.Site, runtime model.NodeRuntime, key string) error {
	return p.Client.Call(ctx, broker.OpApplyNodeRuntime, key, broker.ApplyNodeRuntimeRequest{Site: site, Runtime: runtime}, nil)
}
func (p BrokerProvisioner) ApplyFTPUser(ctx context.Context, site model.Site, user model.FTPUser, key string) error {
	return p.Client.Call(ctx, broker.OpApplyFTPUser, key, broker.ApplyFTPUserRequest{Site: site, User: user}, nil)
}
func (p BrokerProvisioner) DeleteFTPUser(ctx context.Context, site model.Site, user model.FTPUser, key string) error {
	return p.Client.Call(ctx, broker.OpDeleteFTPUser, key, broker.ApplyFTPUserRequest{Site: site, User: user}, nil)
}
func (p BrokerProvisioner) ApplyMailService(ctx context.Context, service model.MailService, key string) error {
	return p.Client.Call(ctx, broker.OpApplyMailService, key, broker.ApplyMailServiceRequest{Service: service}, nil)
}

// processHosting is called from ProcessOne for hosting.* jobs. It loads the
// authoritative desired state instead of trusting job payloads, which keeps
// password hashes out of job metadata and operator-facing activity views.
func (w *Worker) processHosting(ctx context.Context, job store.Job) (bool, error) {
	operator, ok := w.Provisioner.(hostingProvisioner)
	if !ok {
		return true, errors.New("hosting operations are unavailable")
	}
	var operationErr error
	switch job.Kind {
	case "hosting.node_apply":
		site, err := w.Store.Site(ctx, job.TargetID)
		if err != nil {
			operationErr = err
		} else {
			runtime, err := w.Store.NodeRuntime(ctx, site.ID)
			if err != nil {
				operationErr = err
			} else {
				operationErr = operator.ApplyNodeRuntime(ctx, site, runtime, job.IdempotencyKey)
			}
		}
	case "hosting.ftp_apply":
		ftp, err := w.Store.FTPUser(ctx, job.TargetID)
		if err != nil {
			operationErr = err
		} else {
			site, err := w.Store.Site(ctx, ftp.SiteID)
			if err != nil {
				operationErr = err
			} else {
				operationErr = operator.ApplyFTPUser(ctx, site, ftp, job.IdempotencyKey)
			}
		}
	case "hosting.ftp_delete":
		ftp, err := w.Store.FTPUser(ctx, job.TargetID)
		if err != nil {
			operationErr = err
		} else {
			site, err := w.Store.Site(ctx, ftp.SiteID)
			if err != nil {
				operationErr = err
			} else {
				operationErr = operator.DeleteFTPUser(ctx, site, ftp, job.IdempotencyKey)
			}
		}
	case "hosting.mail_apply":
		mail, err := w.Store.MailService(ctx)
		if err != nil {
			operationErr = err
		} else {
			operationErr = operator.ApplyMailService(ctx, mail, job.IdempotencyKey)
		}
	default:
		return false, nil
	}
	return true, operationErr
}
