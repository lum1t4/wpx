package provision

import (
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestRenderPoolUsesOndemandWorkersAndSiteIdentity(t *testing.T) {
	pool := renderPool(
		model.Site{ID: "example-com", PHPVersion: "8.4"},
		Identity{Name: "wpxabc", UID: 1001, GID: 1001},
		"/var/www/wpx/example-com",
		"/run/php/wpx-example-com.sock",
		"/etc/wpx/snippets/php/example-com.conf",
	)
	if !strings.HasPrefix(pool, phpOwnershipMarker) {
		t.Fatalf("pool uses an invalid PHP ownership marker: %q", pool)
	}
	for _, expected := range []string{
		"pm = ondemand", "user = wpxabc", "listen.group = www-data",
		"chdir = /var/www/wpx/example-com", "clear_env = yes",
	} {
		if !strings.Contains(pool, expected) {
			t.Fatalf("pool missing %q: %s", expected, pool)
		}
	}
}
