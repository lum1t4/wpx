//go:build linux

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/rbac"
)

type searchReplaceWebBroker struct {
	calls     int
	tableName string
}

func (b *searchReplaceWebBroker) Call(_ context.Context, operation broker.Operation, _ string, input, output any) error {
	if operation != broker.OpWordPressSearchReplace {
		return nil
	}
	b.calls++
	request := input.(broker.WordPressSearchReplaceRequest)
	if !request.DryRun {
		return nil
	}
	tableName := b.tableName
	if tableName == "" {
		tableName = "wp_posts"
	}
	*output.(*broker.WordPressSearchReplaceResult) = broker.WordPressSearchReplaceResult{
		Tables: 1, Replacements: 3,
		TableResults: []broker.WordPressSearchReplaceTableResult{{Name: tableName, Replacements: 3}},
	}
	return nil
}

func TestWordPressSearchReplaceRequiresPreviewAndQueuesBoundJob(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	privileged := &searchReplaceWebBroker{}
	server.broker = privileged

	page := navigationRequest(t, server, fixture.owner, http.MethodGet, "/sites/fleet-web-one/wordpress/search-replace", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	if strings.Contains(page.Body.String(), `/wordpress/search-replace/apply`) {
		t.Fatal("apply control rendered before a successful preview")
	}

	search := `<script>alert("search")</script>`
	preview := navigationRequest(t, server, fixture.owner, http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/preview", url.Values{
		"search": {search}, "replace": {"safe"}, "target_id": {fixture.targetID},
	})
	requireNavigationStatus(t, preview, http.StatusOK)
	body := preview.Body.String()
	if privileged.calls != 1 || !strings.Contains(body, "Matches in preview") || !strings.Contains(body, `/wordpress/search-replace/apply`) {
		t.Fatalf("successful preview did not render apply controls and counts: %s", body)
	}
	if strings.Contains(body, search) || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("preview did not HTML-escape literal values")
	}
	match := regexp.MustCompile(`name="preview_token" value="([A-Za-z0-9_-]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("successful preview did not return an opaque token")
	}

	apply := navigationRequest(t, server, fixture.owner, http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/apply", url.Values{
		"search": {search}, "replace": {"safe"}, "target_id": {fixture.targetID}, "preview_token": {match[1]}, "confirm": {"yes"},
	})
	if apply.Code != http.StatusSeeOther {
		t.Fatalf("apply status = %d, want 303; body: %s", apply.Code, apply.Body.String())
	}
	jobs, err := server.store.RecentJobsForUser(context.Background(), fixture.owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	queued := false
	for _, job := range jobs {
		if job.Kind == "wordpress.search_replace" && job.Status == "queued" {
			queued = true
			break
		}
	}
	if !queued {
		t.Fatal("apply did not enqueue a durable search and replace job")
	}
	activity := navigationRequest(t, server, fixture.owner, http.MethodGet, "/jobs", nil)
	if strings.Contains(activity.Body.String(), "alert(&#34;search&#34;)") || strings.Contains(activity.Body.String(), search) {
		t.Fatal("Activity exposed search and replace operands")
	}
}

func TestWordPressSearchReplaceRejectsCSRFAndChangedPreview(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	privileged := &searchReplaceWebBroker{}
	server.broker = privileged

	request := httptest.NewRequest(http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/preview", strings.NewReader(url.Values{
		"search": {"old"}, "replace": {"new"}, "target_id": {fixture.targetID},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("id", "fleet-web-one")
	response := httptest.NewRecorder()
	server.previewWordPressSearchReplace(response, request, fixture.owner)
	if response.Code != http.StatusForbidden || privileged.calls != 0 {
		t.Fatalf("missing CSRF status=%d broker calls=%d", response.Code, privileged.calls)
	}
	privileged.tableName = `<img src=x onerror=alert(1)>`
	untrusted := navigationRequest(t, server, fixture.owner, http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/preview", url.Values{
		"search": {"old"}, "replace": {"new"}, "target_id": {fixture.targetID},
	})
	if untrusted.Code != http.StatusInternalServerError || strings.Contains(untrusted.Body.String(), privileged.tableName) {
		t.Fatalf("untrusted broker result status=%d body=%s", untrusted.Code, untrusted.Body.String())
	}
	privileged.tableName = ""

	preview := navigationRequest(t, server, fixture.owner, http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/preview", url.Values{
		"search": {"old"}, "replace": {"new"}, "target_id": {fixture.targetID},
	})
	match := regexp.MustCompile(`name="preview_token" value="([A-Za-z0-9_-]+)"`).FindStringSubmatch(preview.Body.String())
	if len(match) != 2 {
		t.Fatalf("preview status=%d token missing: %s", preview.Code, preview.Body.String())
	}
	apply := navigationRequest(t, server, fixture.owner, http.MethodPost, "/sites/fleet-web-one/wordpress/search-replace/apply", url.Values{
		"search": {"old"}, "replace": {"changed"}, "target_id": {fixture.targetID}, "preview_token": {match[1]}, "confirm": {"yes"},
	})
	if apply.Code != http.StatusConflict || !strings.Contains(apply.Body.String(), "no longer matches") {
		t.Fatalf("changed preview status=%d body=%s", apply.Code, apply.Body.String())
	}

	collaborator, err := server.store.CreateUser(context.Background(), fixture.owner, "search-collaborator", "a-secure-test-password", rbac.Collaborator, []string{"fleet-web-one"})
	if err != nil {
		t.Fatal(err)
	}
	denied := navigationRequest(t, server, collaborator, http.MethodPost, "/sites/fleet-web-two/wordpress/search-replace/preview", url.Values{
		"search": {"old"}, "replace": {"new"}, "target_id": {fixture.targetID},
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unassigned site status = %d, want 403", denied.Code)
	}
}
