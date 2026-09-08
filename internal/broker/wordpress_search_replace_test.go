package broker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type searchReplaceBrokerProvisioner struct {
	fakeHosting
	calls  int
	jobKey string
	result WordPressSearchReplaceResult
	err    error
}

func (provisioner *searchReplaceBrokerProvisioner) SearchReplaceWordPress(_ context.Context, _ model.Site, _ model.BackupTarget, _ model.WordPressSearchReplace, _ bool, jobKey string) (WordPressSearchReplaceResult, error) {
	provisioner.calls++
	provisioner.jobKey = jobKey
	return provisioner.result, provisioner.err
}

func TestWordPressSearchReplaceDispatchUsesEnvelopeKeyAndReturnsRecoveryOnError(t *testing.T) {
	recoveryID := strings.Repeat("a", 64)
	provisioner := &searchReplaceBrokerProvisioner{result: WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, err: errors.New("recovery required")}
	request := WordPressSearchReplaceRequest{
		Site:         model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"},
		BackupTarget: model.BackupTarget{ID: "bkt_test", Name: "Recovery", Kind: model.BackupS3, Status: "active", Endpoint: "https://objects.example.com", Bucket: "wpx", Region: "us-east-1", BucketLookup: "path", AccessKey: "key", SecretKey: "secret", RepositoryPassword: "long-enough-repository-password"},
		Change:       model.WordPressSearchReplace{Search: "old", Replace: "new"},
	}
	payload, _ := json.Marshal(request)
	response, handled := (&Server{Provisioner: provisioner}).dispatchWordPressSearchReplace(Request{ID: "request", IdempotencyKey: "durable-key", Operation: OpWordPressSearchReplace, Payload: payload})
	if !handled || response.OK || provisioner.calls != 1 || provisioner.jobKey != "durable-key" || !strings.Contains(string(response.Result), recoveryID) {
		t.Fatalf("response=%#v calls=%d jobKey=%q", response, provisioner.calls, provisioner.jobKey)
	}
}

func TestWordPressSearchReplacePreviewRejectsBackupCredentials(t *testing.T) {
	provisioner := &searchReplaceBrokerProvisioner{}
	request := WordPressSearchReplaceRequest{
		Site:         model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"},
		BackupTarget: model.BackupTarget{ID: "unexpected"},
		Change:       model.WordPressSearchReplace{Search: "old", Replace: "new"}, DryRun: true,
	}
	payload, _ := json.Marshal(request)
	response, _ := (&Server{Provisioner: provisioner}).dispatchWordPressSearchReplace(Request{ID: "request", IdempotencyKey: "preview-key", Operation: OpWordPressSearchReplace, Payload: payload})
	if response.OK || provisioner.calls != 0 {
		t.Fatalf("preview with backup credentials reached provisioner: %#v", response)
	}
}
