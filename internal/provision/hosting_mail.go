package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) ApplyMailService(ctx context.Context, mail model.MailService) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := model.ValidateMailService(mail); err != nil {
		return err
	}
	if !mail.Enabled {
		return errors.New("mail enable request is required")
	}
	if h.Environment == nil || h.Runner == nil || !filepath.IsAbs(h.PostfixConfigPath) || !filepath.IsAbs(h.PolicyRCDPath) {
		return errors.New("mail dependencies are unavailable")
	}
	previous, existed, err := managedRegularState(h.PostfixConfigPath)
	if err != nil {
		return fmt.Errorf("inspect Postfix configuration: %w", err)
	}
	policy, policyExisted, err := readRegularFile(h.PolicyRCDPath)
	if err != nil {
		return fmt.Errorf("inspect service start policy: %w", err)
	}
	policyMode := os.FileMode(0755)
	if policyExisted {
		if st, statErr := os.Stat(h.PolicyRCDPath); statErr != nil {
			return statErr
		} else {
			policyMode = st.Mode().Perm()
		}
	}
	if err := atomicWrite(h.PolicyRCDPath, []byte("#!/bin/sh\nexit 101\n"), 0755); err != nil {
		return err
	}
	restorePolicy := func() error {
		if policyExisted {
			return atomicWrite(h.PolicyRCDPath, policy, policyMode)
		}
		return os.Remove(h.PolicyRCDPath)
	}
	installErr := h.Environment.RunEnv(ctx, []string{"DEBIAN_FRONTEND=noninteractive"}, "/usr/bin/apt-get", "install", "-y", "--no-install-recommends", "postfix")
	restoreErr := restorePolicy()
	if installErr != nil {
		if restoreErr != nil {
			return fmt.Errorf("install Postfix: %w; restore service start policy: %v", installErr, restoreErr)
		}
		return fmt.Errorf("install Postfix: %w", installErr)
	}
	if restoreErr != nil {
		return fmt.Errorf("restore service start policy: %w", restoreErr)
	}
	config := ownershipMarker + "compatibility_level = 3.6\nmyhostname = " + mail.Hostname + "\ninet_interfaces = loopback-only\ninet_protocols = all\nmynetworks = 127.0.0.0/8 [::1]/128\nrelay_domains =\nsmtpd_relay_restrictions = permit_mynetworks,reject_unauth_destination\n"
	if err := atomicWrite(h.PostfixConfigPath, []byte(config), 0644); err != nil {
		return err
	}
	rollback := func() {
		if existed {
			_ = atomicWrite(h.PostfixConfigPath, previous, 0644)
		}
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/postfix", "check"); err != nil {
		rollback()
		return fmt.Errorf("validate Postfix: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "postfix.service"); err != nil {
		rollback()
		return fmt.Errorf("enable Postfix: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "restart", "postfix.service"); err != nil {
		rollback()
		_ = h.Runner.Run(context.Background(), "/usr/sbin/postfix", "check")
		_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "restart", "postfix.service")
		return fmt.Errorf("restart Postfix: %w", err)
	}
	return nil
}

func readRegularFile(path string) ([]byte, bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("path is not a regular file")
	}
	raw, err := os.ReadFile(path)
	return raw, true, err
}
