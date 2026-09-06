package web

import (
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// canViewSiteSection governs the new tool pages, not the underlying actions.
// Handlers must first check the site's assignment and each POST must still
// authorize its own capability. Hiding a navigation link is never a boundary.
func canViewSiteSection(user store.User, site model.Site, section string) bool {
	switch section {
	case "overview":
		return rbac.Allows(user.Role, rbac.ViewSite)
	case "wordpress":
		return site.Kind == model.WordPress && rbac.Allows(user.Role, rbac.ManageWordPress)
	case "staging":
		return site.Kind == model.WordPress && rbac.Allows(user.Role, rbac.DeploySite)
	case "backups":
		return rbac.Allows(user.Role, rbac.ManageBackups)
	case "security":
		return rbac.Allows(user.Role, rbac.ManageTLS)
	case "settings":
		return rbac.Allows(user.Role, rbac.ManageAllSites)
	default:
		return false
	}
}

// preparePageData keeps the shared shell consistent on ordinary pages and
// validation errors. Site-bearing pages have already authorized the assignment;
// these flags only determine which links and controls to display.
func preparePageData(name string, data *pageData) {
	if data.Form == nil {
		data.Form = make(map[string]string)
	}
	if data.User != nil {
		role := data.User.Role
		data.CanManageUsers = rbac.Allows(role, rbac.ManageUsers)
		data.CanManageServer = rbac.Allows(role, rbac.ManageServer)
		data.CanManageSites = rbac.Allows(role, rbac.ManageAllSites)
		if data.Site != nil {
			data.CanWordPressLogin = data.Site.Kind == model.WordPress && rbac.Allows(role, rbac.WordPressLogin)
			data.CanManageWordPress = data.Site.Kind == model.WordPress && rbac.Allows(role, rbac.ManageWordPress)
			data.CanManageTLS = rbac.Allows(role, rbac.ManageTLS)
			data.CanManageFiles = rbac.Allows(role, rbac.ManageFiles)
			data.CanManageBackups = rbac.Allows(role, rbac.ManageBackups)
			data.CanDeploySite = rbac.Allows(role, rbac.DeploySite)
			data.CanViewLogs = rbac.Allows(role, rbac.ViewLogs)
			data.CanManageDNS = rbac.Allows(role, rbac.ManageDNS)
		}
	}
	data.SiteCount = len(data.Sites)
	for _, site := range data.Sites {
		if site.Status == "active" {
			data.ActiveSiteCount++
		}
		if strings.Contains(site.Status, "failed") || site.Status == "degraded" || site.TLSStatus == "failed" {
			data.AttentionSiteCount++
		}
	}
	if data.Site != nil {
		data.NavSection = "sites"
		if data.Section == "" {
			switch name {
			case "files.html":
				data.Section = "files"
			case "site_dns.html":
				data.Section = "dns"
			case "observability.html":
				data.Section = "observability"
			case "wordpress_health.html":
				data.Section = "wordpress"
			case "staging_created.html", "staging_deploy.html":
				data.Section = "staging"
			default:
				data.Section = "overview"
			}
		}
		return
	}
	switch name {
	case "dashboard.html":
		data.NavSection = "overview"
	case "monitoring.html":
		data.NavSection = "monitoring"
	case "databases.html":
		data.NavSection = "databases"
	case "sites.html":
		data.NavSection = "sites"
	case "jobs.html":
		data.NavSection = "activity"
	case "backup_targets.html":
		data.NavSection = "storage"
	case "dns_providers.html":
		data.NavSection = "dns"
	case "users.html", "user_edit.html":
		data.NavSection = "users"
	case "security.html":
		data.NavSection = "account"
	}
}
