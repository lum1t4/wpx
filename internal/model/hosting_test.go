package model

import "testing"

func TestValidateNodeRuntimeBindsReverseProxy(t *testing.T) {
	site := Site{ID: "node-app", Domain: "node.example.com", Kind: ReverseProxy, Upstream: "http://127.0.0.1:3100"}
	valid := NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Port: 3100, NodeVersion: "24.20.0", Arguments: []string{"--production"}}
	if err := ValidateNodeRuntime(site, valid); err != nil {
		t.Fatal(err)
	}
	for _, entrypoint := range []string{"../server.js", "/tmp/server.js", "dir/../server.js", "server.js\nX=1"} {
		invalid := valid
		invalid.Entrypoint = entrypoint
		if ValidateNodeRuntime(site, invalid) == nil {
			t.Fatalf("accepted entrypoint %q", entrypoint)
		}
	}
}

func TestValidateFTPUserRequiresCryptHash(t *testing.T) {
	if err := ValidateFTPUser(FTPUser{SiteID: "node-app", Username: "deploy", PasswordHash: "$2a$12$01234567890123456789012345678901234567890123456789012"}); err != nil {
		t.Fatal(err)
	}
	if ValidateFTPUser(FTPUser{SiteID: "node-app", Username: "deploy", PasswordHash: "plaintext"}) == nil {
		t.Fatal("accepted plaintext")
	}
}
