package model

import "testing"

func TestValidateS3BackupTarget(t *testing.T) {
	target := BackupTarget{
		Name: "Primary storage", Kind: BackupS3, Endpoint: "https://objects.example.com",
		Bucket: "customer-backups", Prefix: "servers/one", Region: "eu-west-1",
		BucketLookup: "path", AccessKey: "access", SecretKey: "secret",
		RepositoryPassword: "a-long-portable-repository-password",
	}
	if err := ValidateBackupTarget(target); err != nil {
		t.Fatal(err)
	}
	if got := target.RepositoryURL(); got != "s3:https://objects.example.com/customer-backups/servers/one" {
		t.Fatalf("unexpected repository URL %q", got)
	}
	target.Endpoint = "http://objects.example.com"
	if err := ValidateBackupTarget(target); err == nil {
		t.Fatal("insecure endpoint was accepted without confirmation")
	}
	target.AllowInsecureHTTP = true
	if err := ValidateBackupTarget(target); err != nil {
		t.Fatalf("confirmed local HTTP endpoint was rejected: %v", err)
	}
}

func TestGoogleDriveBackupRequiresUserOAuthAndUsesRcloneRepository(t *testing.T) {
	target := BackupTarget{
		Name: "Drive", Kind: BackupGoogleDrive, DriveFolder: "WPX Backups/server-one",
		GoogleClientID: "123-example.apps.googleusercontent.com", GoogleClientSecret: "client-secret-value",
		GoogleToken:        `{"access_token":"short-lived","refresh_token":"refresh-token","token_type":"Bearer"}`,
		RepositoryPassword: "a-repository-password-long-enough",
	}
	if err := ValidateBackupTarget(target); err != nil {
		t.Fatal(err)
	}
	if got := target.RepositoryURL(); got != "rclone:wpxdrive:WPX Backups/server-one" {
		t.Fatalf("unexpected repository URL %q", got)
	}
}
