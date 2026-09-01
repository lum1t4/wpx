// Package rbac defines capabilities independently from HTTP routes. Roles are
// named presets; authorization always asks for a capability so future custom
// roles do not require rewriting business logic.
package rbac

type Role string

const (
	Owner         Role = "owner"
	Administrator Role = "administrator"
	Collaborator  Role = "collaborator"
	Customer      Role = "customer"
)

type Capability string

const (
	ManageOwnership Capability = "ownership.manage"
	ManageServer    Capability = "server.manage"
	ManageUsers     Capability = "users.manage"
	ManageAllSites  Capability = "sites.manage_all"
	ViewSite        Capability = "site.view"
	DeploySite      Capability = "site.deploy"
	ManageBackups   Capability = "site.backups"
	ManageFiles     Capability = "site.files"
	ManageDNS       Capability = "site.dns"
	ViewLogs        Capability = "site.logs"
	WordPressLogin  Capability = "site.wordpress_login"
	ManageTLS       Capability = "site.tls"
	ManageWordPress Capability = "site.wordpress_manage"
)

var presets = map[Role]map[Capability]struct{}{
	Owner: set(ManageOwnership, ManageServer, ManageUsers, ManageAllSites, ViewSite,
		DeploySite, ManageBackups, ManageFiles, ManageDNS, ViewLogs, WordPressLogin, ManageTLS, ManageWordPress),
	Administrator: set(ManageServer, ManageUsers, ManageAllSites, ViewSite,
		DeploySite, ManageBackups, ManageFiles, ManageDNS, ViewLogs, WordPressLogin, ManageTLS, ManageWordPress),
	Collaborator: set(ViewSite, DeploySite, ManageBackups, ManageFiles, ManageDNS,
		ViewLogs, WordPressLogin, ManageTLS, ManageWordPress),
	Customer: set(ViewSite, ManageBackups, ViewLogs, WordPressLogin),
}

func set(values ...Capability) map[Capability]struct{} {
	m := make(map[Capability]struct{}, len(values))
	for _, value := range values {
		m[value] = struct{}{}
	}
	return m
}

func ValidRole(role Role) bool {
	_, ok := presets[role]
	return ok
}

func Allows(role Role, capability Capability) bool {
	_, ok := presets[role][capability]
	return ok
}

func Capabilities(role Role) []Capability {
	values := presets[role]
	result := make([]Capability, 0, len(values))
	for capability := range values {
		result = append(result, capability)
	}
	return result
}
