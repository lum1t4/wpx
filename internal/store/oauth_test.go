package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestGoogleDriveOAuthFlowIsEncryptedAndSingleUse(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	target := model.BackupTarget{Name: "Drive", DriveFolder: "WPX Backups", GoogleClientID: "123.apps.googleusercontent.com", GoogleClientSecret: "very-secret-client-value"}
	stateValue, verifier, err := state.CreateGoogleDriveOAuthFlow(ctx, owner, target, "https://panel.example.com/backups/google-drive/callback")
	if err != nil {
		t.Fatal(err)
	}
	if stateValue == "" || verifier == "" || stateValue == verifier {
		t.Fatal("OAuth state and PKCE verifier were not independently generated")
	}
	var stateHash, ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT state_hash,config_ciphertext FROM oauth_flows`).Scan(&stateHash, &ciphertext); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{stateValue, verifier, target.GoogleClientSecret} {
		if bytes.Contains(stateHash, []byte(secret)) || bytes.Contains(ciphertext, []byte(secret)) {
			t.Fatalf("OAuth secret %q was stored in plaintext", secret)
		}
	}
	flow, err := state.ConsumeGoogleDriveOAuthFlow(ctx, stateValue)
	if err != nil || flow.Actor.ID != owner.ID || flow.Verifier != verifier || flow.Target.GoogleClientSecret != target.GoogleClientSecret {
		t.Fatalf("flow=%#v err=%v", flow, err)
	}
	if _, err := state.ConsumeGoogleDriveOAuthFlow(ctx, stateValue); err == nil {
		t.Fatal("OAuth state was accepted twice")
	}
}
