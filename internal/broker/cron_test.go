package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type recordingCronManager struct {
	SiteProvisioner
	calls int
}

func (m *recordingCronManager) ApplyCronSchedule(context.Context, model.Site, model.CronSchedule, bool) error {
	m.calls++
	return nil
}
func (m *recordingCronManager) ApplyWordPressCronReplacement(context.Context, model.Site, model.WordPressCronSetting) error {
	m.calls++
	return nil
}

func TestCronBrokerRejectsCrossSiteAndUnsafeCommands(t *testing.T) {
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "active"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "queue", Expression: "* * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
	for _, scenario := range []string{"valid", "cross-site", "shell", "traversal", "unknown-field"} {
		t.Run(scenario, func(t *testing.T) {
			manager := &recordingCronManager{}
			payload := ApplyCronRequest{Site: site, Schedule: schedule}
			switch scenario {
			case "cross-site":
				payload.Schedule.SiteID = "other-site"
			case "shell":
				payload.Schedule.Command = []string{"/bin/sh", "-c", "id"}
			case "traversal":
				payload.Site.ID = "../../root"
			}
			encoded, _ := json.Marshal(payload)
			if scenario == "unknown-field" {
				encoded = append(encoded[:len(encoded)-1], []byte(`,"root_command":"id"}`)...)
			}
			responsegetline, handled := (&Server{Provisioner: manager}).dispatchCron(Request{Operation: OpCronApply, Payload: encoded})
			if !handled {
				t.Fatal("cron operation was not handled")
			}
			if scenario == "valid" && (!responsegetline.OK || manager.calls != 1) {
				t.Fatalf("valid request rejected: %+v", responsegetline)
			}
			if scenario != "valid" && (responsegetline.OK || manager.calls != 0) {
				t.Fatalf("unsafe request accepted: %+v", responsegetline)
			}
		})
	}
}
