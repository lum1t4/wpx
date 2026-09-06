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

func TestPHPPackagesRespectBundledOPcacheInPHP85(t *testing.T) {
	for _, version := range []string{"7.4", "8.4", "8.5"} {
		packages := strings.Join(phpPackages(version), " ")
		if strings.Contains(packages, "php"+version+"-opcache") != (version != "8.5") {
			t.Fatalf("PHP %s OPcache packaging is wrong: %s", version, packages)
		}
		for _, required := range []string{"fpm", "cli", "mysql", "redis"} {
			if !strings.Contains(packages, "php"+version+"-"+required) {
				t.Fatalf("PHP %s is missing required %s package", version, required)
			}
		}
	}
}
