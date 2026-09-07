package provision

import (
	"context"

	"github.com/lum1t4/wpx/internal/model"
)

// decorateSiteConfig is shared by ordinary provisioning, TLS generation, and
// domain changes. Controls are keyed by immutable site identity so rebuilding a
// virtual host cannot silently remove protection or inherit another site's gate.
// Callers already hold the host lock; these helpers must not acquire it again.
func (h *Host) decorateSiteConfig(site model.Site, configuration string) (string, error) {
	if err := h.EnsureAccessDefaults(context.Background(), site); err != nil {
		return "", err
	}
	configuration, err := h.InjectSiteAccessNginx(site, configuration)
	if err != nil {
		return "", err
	}
	return h.InjectSiteSecurityNginx(site, configuration)
}
