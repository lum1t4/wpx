package model

import "errors"

// SecuritySettings is the desired per-site protection policy. The individual
// controls retain their values while Enabled is false so an operator can turn
// the same policy back on without reconstructing it.
type SecuritySettings struct {
	Enabled                 bool   `json:"enabled"`
	LoginProtection         bool   `json:"login_protection"`
	XMLRPCProtection        bool   `json:"xmlrpc_protection"`
	SensitivePathProtection bool   `json:"sensitive_path_protection"`
	Burst404Protection      bool   `json:"burst_404_protection"`
	Status                  string `json:"status,omitempty"`
	LastError               string `json:"last_error,omitempty"`
	Generation              int64  `json:"generation,omitempty"`
}

func DefaultSecuritySettings() SecuritySettings {
	return SecuritySettings{
		LoginProtection:         true,
		XMLRPCProtection:        true,
		SensitivePathProtection: true,
		Burst404Protection:      true,
		Status:                  "not_configured",
	}
}

func ValidateSecuritySettings(site Site, settings SecuritySettings) error {
	if err := ValidateSite(site); err != nil {
		return err
	}
	if site.Kind != WordPress {
		return errors.New("security defense is available only for WordPress sites")
	}
	if site.Status != "active" && site.Status != "disabled" {
		return errors.New("site must be active or disabled to change security defense")
	}
	if settings.Enabled && !settings.LoginProtection && !settings.XMLRPCProtection && !settings.SensitivePathProtection && !settings.Burst404Protection {
		return errors.New("enable at least one protection")
	}
	return nil
}
