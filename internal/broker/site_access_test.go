package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/crypto/bcrypt"
)

type accessManager struct {
	SiteProvisioner
	calls int
}

func (m *accessManager) ApplySiteAccess(context.Context, model.Site, model.SiteAccessSettings) error {
	m.calls++
	return nil
}

func TestSiteAccessBrokerRejectsTraversalAndMalformedCredentials(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("a-long-test-password"), 12)
	for _, scenario := range []string{"valid", "traversal", "mismatched site", "bad username", "unknown field"} {
		t.Run(scenario, func(t *testing.T) {
			manager := &accessManager{}
			server := &Server{Provisioner: manager}
			payload := ApplySiteAccessRequest{
				Site:     model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static, Status: "active"},
				Settings: model.SiteAccessSettings{SiteID: "access-site", BasicAuthEnabled: true, Username: "visitor", PasswordHash: string(hash), Status: "pending"},
			}
			switch scenario {
			case "traversal":
				payload.Site.ID = "../../outside"
				payload.Settings.SiteID = payload.Site.ID
			case "mismatched site":
				payload.Settings.SiteID = "other-site"
			case "bad username":
				payload.Settings.Username = "bad:user"
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unknown field" {
				encoded = append(encoded[:len(encoded)-1], []byte(`,"command":"unsafe"}`)...)
			}
			response, handled := server.dispatchSiteAccess(Request{Operation: OpApplySiteAccess, Payload: encoded})
			if !handled {
				t.Fatal("access request was not handled")
			}
			if scenario == "valid" {
				if !response.OK || manager.calls != 1 {
					t.Fatalf("valid request rejected: %#v", response)
				}
			} else if response.OK || manager.calls != 0 {
				t.Fatalf("unsafe request crossed root boundary: %#v", response)
			}
		})
	}
}
