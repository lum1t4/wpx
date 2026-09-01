package provision

import (
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestPythonServiceUsesSocketActivationAndSiteIdentity(t *testing.T) {
	site := model.Site{ID: "example-com"}
	identity := Identity{Name: "wpxabc"}
	socket := renderPythonSocket(site, identity, "/run/wpx-sites/example-com.sock")
	service := renderPythonService(site, identity, "/var/www/wpx/example-com/public", "/var/www/wpx/example-com/venv/bin/python")
	for _, expected := range []string{"SocketGroup=www-data", "SocketMode=0660", "RemoveOnStop=true"} {
		if !strings.Contains(socket, expected) {
			t.Fatalf("socket unit missing %q: %s", expected, socket)
		}
	}
	for _, expected := range []string{"User=wpxabc", "--bind fd://3", "NoNewPrivileges=true", "ProtectSystem=strict"} {
		if !strings.Contains(service, expected) {
			t.Fatalf("service unit missing %q: %s", expected, service)
		}
	}
}
