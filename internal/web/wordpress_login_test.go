//go:build linux

package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

func TestWordPressLoginCapabilityAppearsOnlyInLocationFragment(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	site := model.Site{ID: "login-site", Domain: "login.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	navigationSite(t, server, owner, site)
	token := strings.Repeat("a", 64)
	privileged.run = func(operation broker.Operation, _, output any) error {
		if operation != broker.OpWordPressLogin {
			return nil
		}
		*output.(*broker.WordPressLoginResult) = broker.WordPressLoginResult{URL: "http://login.example.com/wpx-login-handler.php#" + token}
		return nil
	}
	response := navigationRequest(t, server, owner, http.MethodPost, "/sites/login-site/wordpress/login", url.Values{})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Location") != "http://login.example.com/wpx-login-handler.php#"+token || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected redirect headers: %#v", response.Header())
	}
	if response.Body.Len() != 0 || strings.Contains(response.Body.String(), token) {
		t.Fatal("login capability leaked into the response body")
	}
}

func TestWordPressLoginRejectsUnscopedBrokerURL(t *testing.T) {
	for name, target := range map[string]string{
		"wrong host":  "http://attacker.example/wpx-login-handler.php#" + strings.Repeat("a", 64),
		"query token": "http://login.example.com/wpx-login-handler.php?token=" + strings.Repeat("a", 64),
		"wrong path":  "http://login.example.com/wp-login.php#" + strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			site := model.Site{ID: "login-site", Domain: "login.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
			navigationSite(t, server, owner, site)
			privileged.run = func(operation broker.Operation, _, output any) error {
				if operation == broker.OpWordPressLogin {
					*output.(*broker.WordPressLoginResult) = broker.WordPressLoginResult{URL: target}
				}
				return nil
			}
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/login-site/wordpress/login", url.Values{})
			if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), strings.Repeat("a", 64)) {
				t.Fatalf("unsafe broker URL response = %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}
