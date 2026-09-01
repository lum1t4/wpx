package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestApplySnippetsWritesOwnedIncludesAndSyntaxTestsBeforeReload(t *testing.T) {
	runner := &recordRunner{}
	host := testHost(t, runner)
	site := model.Site{ID: "php-example", Domain: "php.example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "active"}
	snippets := model.SiteSnippets{Nginx: "client_max_body_size 128M;", PHP: "php_admin_value[memory_limit] = 512M"}
	if err := host.ApplySnippets(context.Background(), site, snippets); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string][2]string{
		filepath.Join(host.NginxSnippetRoot, site.ID+".conf"): {ownershipMarker, "client_max_body_size 128M;"},
		filepath.Join(host.PHPSnippetRoot, site.ID+".conf"):   {phpOwnershipMarker, "php_admin_value[memory_limit] = 512M"},
	} {
		content, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(content), expected[0]) || !strings.Contains(string(content), expected[1]) {
			t.Fatalf("snippet %s content=%q err=%v", path, content, err)
		}
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, strings.Join(call, " "))
	}
	joined := strings.Join(commands, "\n")
	nginxTest := strings.Index(joined, "/usr/sbin/nginx -t")
	phpTest := strings.Index(joined, "/usr/sbin/php-fpm8.4 -t")
	nginxReload := strings.Index(joined, "/usr/bin/systemctl reload nginx.service")
	if nginxTest < 0 || phpTest < nginxTest || nginxReload < phpTest {
		t.Fatalf("unsafe validation/reload order: %s", joined)
	}
}
