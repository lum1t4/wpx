package model

import (
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
)

type BackupKind string

const (
	BackupS3          BackupKind = "s3"
	BackupGoogleDrive BackupKind = "google_drive"
)

type BackupTarget struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Kind               BackupKind `json:"kind"`
	Status             string     `json:"status"`
	Endpoint           string     `json:"endpoint,omitempty"`
	Bucket             string     `json:"bucket,omitempty"`
	Prefix             string     `json:"prefix,omitempty"`
	Region             string     `json:"region,omitempty"`
	BucketLookup       string     `json:"bucket_lookup,omitempty"`
	AccessKey          string     `json:"access_key,omitempty"`
	SecretKey          string     `json:"secret_key,omitempty"`
	AllowInsecureHTTP  bool       `json:"allow_insecure_http,omitempty"`
	RepositoryPassword string     `json:"repository_password,omitempty"`
	DriveFolder        string     `json:"drive_folder,omitempty"`
	GoogleClientID     string     `json:"google_client_id,omitempty"`
	GoogleClientSecret string     `json:"google_client_secret,omitempty"`
	GoogleToken        string     `json:"google_token,omitempty"`
	GoogleSharedDrive  string     `json:"google_shared_drive,omitempty"`
}

type BackupSnapshot struct {
	ID               string `json:"id"`
	SiteID           string `json:"site_id"`
	TargetID         string `json:"target_id"`
	ResticSnapshotID string `json:"restic_snapshot_id"`
	CreatedAt        string `json:"created_at"`
}

type BackupSchedule struct {
	ID                      string          `json:"id"`
	SiteID                  string          `json:"site_id"`
	TargetID                string          `json:"target_id"`
	IntervalHours           int             `json:"interval_hours"`
	NextRun                 string          `json:"next_run"`
	Enabled                 bool            `json:"enabled"`
	Retention               BackupRetention `json:"retention"`
	RestoreTestIntervalDays int             `json:"restore_test_interval_days"`
	NextRestoreTest         string          `json:"next_restore_test,omitempty"`
	LastRestoreTestAt       string          `json:"last_restore_test_at,omitempty"`
	LastRestoreTestStatus   string          `json:"last_restore_test_status,omitempty"`
}

type BackupRetention struct {
	KeepDaily   int `json:"keep_daily,omitempty"`
	KeepWeekly  int `json:"keep_weekly,omitempty"`
	KeepMonthly int `json:"keep_monthly,omitempty"`
}

func ValidateBackupRetention(retention BackupRetention, allowEmpty bool) error {
	if retention.KeepDaily < 0 || retention.KeepDaily > 365 || retention.KeepWeekly < 0 || retention.KeepWeekly > 260 || retention.KeepMonthly < 0 || retention.KeepMonthly > 120 {
		return errors.New("backup retention values are outside safe limits")
	}
	if !allowEmpty && retention.KeepDaily+retention.KeepWeekly+retention.KeepMonthly == 0 {
		return errors.New("keep at least one daily, weekly, or monthly backup")
	}
	return nil
}

var bucketPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,61}[a-zA-Z0-9]$`)
var regionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)
var resticSnapshotPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidResticSnapshotID(id string) bool { return resticSnapshotPattern.MatchString(id) }

func ValidateBackupTarget(target BackupTarget) error {
	if len(strings.TrimSpace(target.Name)) < 2 || len(target.Name) > 64 {
		return errors.New("backup target name must be 2-64 characters")
	}
	if len(target.RepositoryPassword) < 20 {
		return errors.New("repository password must contain at least 20 characters")
	}
	switch target.Kind {
	case BackupS3:
		endpoint, err := url.Parse(target.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
			return errors.New("S3 endpoint must contain only an http(s) scheme and host")
		}
		if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && target.AllowInsecureHTTP) {
			return errors.New("S3 endpoint must use HTTPS unless insecure HTTP is explicitly confirmed")
		}
		if !bucketPattern.MatchString(target.Bucket) {
			return errors.New("invalid S3 bucket name")
		}
		if target.Region == "" {
			target.Region = "us-east-1"
		}
		if !regionPattern.MatchString(target.Region) {
			return errors.New("invalid S3 region")
		}
		if target.BucketLookup != "auto" && target.BucketLookup != "path" && target.BucketLookup != "dns" {
			return errors.New("bucket lookup must be auto, path, or dns")
		}
		if target.AccessKey == "" || target.SecretKey == "" {
			return errors.New("S3 credentials are required")
		}
		prefix := strings.Trim(target.Prefix, "/")
		if prefix != "" && (path.Clean(prefix) != prefix || prefix == ".." || strings.HasPrefix(prefix, "../")) {
			return errors.New("invalid repository prefix")
		}
	case BackupGoogleDrive:
		if err := ValidateGoogleDriveSetup(target); err != nil {
			return err
		}
		var token struct {
			RefreshToken string `json:"refresh_token"`
		}
		if json.Unmarshal([]byte(target.GoogleToken), &token) != nil || token.RefreshToken == "" {
			return errors.New("Google authorization token must include a refresh token")
		}
	default:
		return errors.New("invalid backup target kind")
	}
	return nil
}

// ValidateGoogleDriveSetup checks the user-entered fields before WPX sends the
// browser to Google. The access and refresh tokens do not exist at that point;
// ValidateBackupTarget performs the additional token check after the callback.
func ValidateGoogleDriveSetup(target BackupTarget) error {
	if len(strings.TrimSpace(target.Name)) < 2 || len(target.Name) > 64 {
		return errors.New("backup target name must be 2-64 characters")
	}
	if target.RepositoryPassword != "" && len(target.RepositoryPassword) < 20 {
		return errors.New("repository password must contain at least 20 characters")
	}
	folder := strings.Trim(target.DriveFolder, "/")
	if folder == "" || len(folder) > 512 || path.Clean(folder) != folder || strings.HasPrefix(folder, "../") || strings.ContainsAny(folder, "\r\n:") {
		return errors.New("invalid Google Drive backup folder")
	}
	if !strings.HasSuffix(target.GoogleClientID, ".apps.googleusercontent.com") || len(target.GoogleClientID) > 256 || len(target.GoogleClientSecret) < 12 || len(target.GoogleClientSecret) > 512 || strings.ContainsAny(target.GoogleClientID+target.GoogleClientSecret, "\r\n") {
		return errors.New("a user-owned Google OAuth client is required")
	}
	if target.GoogleSharedDrive != "" && !providerIDPattern.MatchString(target.GoogleSharedDrive) {
		return errors.New("invalid Shared Drive identifier")
	}
	return nil
}

func (target BackupTarget) RepositoryURL() string {
	if target.Kind == BackupGoogleDrive {
		return "rclone:wpxdrive:" + strings.Trim(target.DriveFolder, "/")
	}
	base := "s3:" + strings.TrimRight(target.Endpoint, "/") + "/" + target.Bucket
	if prefix := strings.Trim(target.Prefix, "/"); prefix != "" {
		base += "/" + prefix
	}
	return base
}
