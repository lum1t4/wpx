package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type phpVersionManager struct {
	SiteProvisioner
	calls int
}

func (p *phpVersionManager) ChangePHPVersion(context.Context, model.Site, model.PHPVersionChange) error {
	p.calls++
	return nil
}

func TestPHPVersionBrokerValidatesBeforeCallingRootOperation(t *testing.T) {
	for _, scenario := range []string{"valid", "traversal", "unsupported version", "unconfirmed EOL", "wrong current version", "wrong kind", "wrong status", "unknown field"} {
		t.Run(scenario, func(t *testing.T) {
			manager := &phpVersionManager{}
			server := &Server{Provisioner: manager}
			payload := ChangePHPVersionRequest{
				Site:   model.Site{ID: "php-site", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "php_changing"},
				Change: model.PHPVersionChange{PreviousVersion: "8.4", Version: "8.5"},
			}
			switch scenario {
			case "traversal":
				payload.Site.ID = "../../outside"
			case "unsupported version":
				payload.Change.Version = "9.0"
			case "unconfirmed EOL":
				payload.Change.Version = "7.4"
			case "wrong current version":
				payload.Change.PreviousVersion = "8.3"
			case "wrong kind":
				payload.Site.Kind, payload.Site.PHPVersion = model.Static, ""
			case "wrong status":
				payload.Site.Status = "active"
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unknown field" {
				encoded = append(encoded[:len(encoded)-1], []byte(`,"command":"unsafe"}`)...)
			}
			response := server.dispatch(Request{Version: ProtocolVersion, Operation: OpChangePHPVersion, Payload: encoded})
			if scenario == "valid" {
				if !response.OK || manager.calls != 1 {
					t.Fatalf("valid change rejected: %+v", response)
				}
			} else if response.OK || manager.calls != 0 {
				t.Fatalf("unsafe request crossed root boundary: %+v", response)
			}
		})
	}
}
