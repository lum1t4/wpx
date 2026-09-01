package install

import (
	"fmt"
	"os"
	"path/filepath"
)

func installLogPolicy() error {
	if err := atomicWrite("/etc/logrotate.d/wpx", []byte(nginxLogrotatePolicy()), 0644); err != nil {
		return fmt.Errorf("install WPX log rotation: %w", err)
	}
	journalDirectory := "/etc/systemd/journald@wpx.conf.d"
	if err := os.MkdirAll(journalDirectory, 0755); err != nil {
		return fmt.Errorf("prepare WPX journal limits: %w", err)
	}
	if err := atomicWrite(filepath.Join(journalDirectory, "limits.conf"), []byte(wpxJournalPolicy()), 0644); err != nil {
		return fmt.Errorf("install WPX journal limits: %w", err)
	}
	return nil
}

func nginxLogrotatePolicy() string {
	return `/var/log/nginx/wpx-*-access.log /var/log/nginx/wpx-*-error.log {
    daily
    maxsize 100M
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    sharedscripts
    postrotate
        /usr/bin/systemctl reload nginx.service >/dev/null 2>&1 || true
    endscript
}
`
}

func wpxJournalPolicy() string {
	return `[Journal]
Storage=persistent
SystemMaxUse=256M
RuntimeMaxUse=64M
MaxRetentionSec=14day
`
}
