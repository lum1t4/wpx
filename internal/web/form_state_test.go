//go:build linux

package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

// A failed mutation must leave the work the user can safely retry in the form.
// These tests check rendered responses, not only the data passed to templates:
// losing content or selections in either layer has the same cost to the user.
func TestFailedFileSavePreservesEditor(t *testing.T) {
	for _, failList := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory-list-fails=%t", failList), func(t *testing.T) {
			server, owner, privileged := navigationServer(t)
			navigationSite(t, server, owner, model.Site{ID: "editor-site", Domain: "editor.example.com", Kind: model.Static})
			const directory = "assets & files"
			const filename = directory + "/unfinished + page.php"
			const content = "<?php\n// Unfinished draft; keep every byte.\necho 'a & b';\n</textarea><script>alert('draft')</script>\n"
			writes := 0
			privileged.run = func(op broker.Operation, in, out any) error {
				switch op {
				case broker.OpFileWrite:
					writes++
					request := in.(broker.FileWriteRequest)
					if request.Path != filename || request.Content != content {
						t.Errorf("write request lost submitted work: path=%q content=%q", request.Path, request.Content)
					}
					return errors.New("PHP syntax check failed")
				case broker.OpFileList:
					if got := in.(broker.FileRequest).Path; got != directory {
						t.Errorf("listed directory = %q, want %q", got, directory)
					}
					if failList {
						return errors.New("directory unavailable")
					}
					*out.(*broker.FileListResult) = broker.FileListResult{}
					return nil
				default:
					return fmt.Errorf("unexpected operation after failed save: %s", op)
				}
			}
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites/editor-site/files", url.Values{"path": {filename}, "content": {content}})
			requireNavigationStatus(t, response, http.StatusBadRequest)
			body := response.Body.String()
			if writes != 1 {
				t.Errorf("file write attempts = %d, want 1", writes)
			}
			if got := navigationInputValue(body, "path"); got != filename {
				t.Errorf("editor path after failed save = %q, want %q", got, filename)
			}
			if got := formTextareaValue(body, "content"); got != content {
				t.Errorf("editor content after failed save = %q, want %q", got, content)
			}
			if strings.Contains(body, "<script>alert('draft')</script>") {
				t.Error("submitted source escaped the text editor and became executable HTML")
			}
			if !strings.Contains(body, `action="/sites/editor-site/files"`) || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "</html>") {
				t.Error("failed save did not return a complete, editable form with an error")
			}
			for _, op := range privileged.calls {
				if op == broker.OpFileRead {
					t.Error("failed save reread old file contents instead of keeping the draft")
				}
			}
		})
	}
}

func TestCreateUserMismatchPreservesNonSecretFields(t *testing.T) {
	server, owner, _ := navigationServer(t)
	for _, id := range []string{"selected-site", "unselected-site"} {
		navigationSite(t, server, owner, model.Site{ID: id, Domain: id + ".example.com", Kind: model.Static})
	}
	form := url.Values{
		"username":         {"new-customer"},
		"role":             {"customer"},
		"site_ids":         {"selected-site"},
		"password":         {"secret-password-one"},
		"password_confirm": {"secret-password-two"},
	}
	response := navigationRequest(t, server, owner, http.MethodPost, "/users", form)
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body := response.Body.String()
	if got := navigationInputValue(body, "username"); got != form.Get("username") {
		t.Errorf("username after validation = %q, want %q", got, form.Get("username"))
	}
	if got := navigationSelectedValue(body, "role"); got != form.Get("role") {
		t.Errorf("role after validation = %q, want %q", got, form.Get("role"))
	}
	formRequireCheckbox(t, body, "site_ids", "selected-site", true)
	formRequireCheckbox(t, body, "site_ids", "unselected-site", false)
	for _, name := range []string{"password", "password_confirm"} {
		if got := navigationInputValue(body, name); got != "" || strings.Contains(body, form.Get(name)) {
			t.Errorf("validation response echoed secret field %q", name)
		}
	}
	if !strings.Contains(body, `action="/users"`) || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "</html>") {
		t.Fatal("validation error did not return a complete, editable user creation form")
	}
	users, err := server.store.ListUsers(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("mismatched passwords created a user: users=%d error=%v", len(users), err)
	}
}

func TestFailedUserEditPreservesSubmittedAccess(t *testing.T) {
	server, owner, _ := navigationServer(t)
	for _, id := range []string{"original-site", "replacement-site"} {
		navigationSite(t, server, owner, model.Site{ID: id, Domain: id + ".example.com", Kind: model.Static})
	}
	ctx := context.Background()
	target, err := server.store.CreateUser(ctx, owner, "existing-collaborator", "form-test-password", rbac.Collaborator, []string{"original-site"})
	if err != nil {
		t.Fatal(err)
	}
	// A stale site selection makes the store reject the update. The valid
	// selection and changed role must still be present so the user can retry.
	response := navigationRequest(t, server, owner, http.MethodPost, "/users/"+target.ID, url.Values{
		"role":     {"customer"},
		"site_ids": {"replacement-site", "missing-site"},
	})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	body := response.Body.String()
	if got := navigationSelectedValue(body, "role"); got != "customer" {
		t.Errorf("role after failed update = %q, want customer", got)
	}
	formRequireCheckbox(t, body, "site_ids", "original-site", false)
	formRequireCheckbox(t, body, "site_ids", "replacement-site", true)
	if !strings.Contains(body, target.Username) || !strings.Contains(body, `action="/users/`+target.ID+`"`) || !strings.Contains(body, "</html>") {
		t.Fatal("failed update lost the user identity or editable access form")
	}
	persisted, err := server.store.User(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Role != rbac.Collaborator || len(persisted.SiteIDs) != 1 || persisted.SiteIDs[0] != "original-site" {
		t.Fatalf("failed update changed stored access: role=%q sites=%v", persisted.Role, persisted.SiteIDs)
	}
}

func TestInvalidTOTPConfirmationPreservesEnrollment(t *testing.T) {
	server, owner, _ := navigationServer(t)
	begin := navigationRequest(t, server, owner, http.MethodPost, "/account/totp/begin", url.Values{})
	requireNavigationStatus(t, begin, http.StatusOK)
	enrollment, err := server.store.PendingTOTPEnrollment(context.Background(), owner)
	if err != nil || enrollment.Secret == "" {
		t.Fatalf("begin enrollment did not retain a pending secret: error=%v", err)
	}
	failed := navigationRequest(t, server, owner, http.MethodPost, "/account/totp/confirm", url.Values{"code": {"invalid"}})
	requireNavigationStatus(t, failed, http.StatusBadRequest)
	for name, body := range map[string]string{
		"begin":   begin.Body.String(),
		"failure": failed.Body.String(),
		"refresh": navigationRequest(t, server, owner, http.MethodGet, "/account/security", nil).Body.String(),
	} {
		t.Run(name, func(t *testing.T) {
			secretInput := ""
			for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
				if navigationAttribute(tag, "id") == "totp-secret" {
					secretInput = navigationAttribute(tag, "value")
				}
			}
			if secretInput != enrollment.Secret {
				t.Error("enrollment secret changed or disappeared from the confirmation page")
			}
			if !strings.Contains(body, `action="/account/totp/confirm"`) || !strings.Contains(body, `name="code"`) || !strings.Contains(body, "</html>") {
				t.Error("enrollment no longer offers a complete confirmation form")
			}
		})
	}
	persisted, err := server.store.User(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.TOTPEnabled {
		t.Fatal("invalid confirmation enabled TOTP")
	}
	retained, err := server.store.PendingTOTPEnrollment(context.Background(), owner)
	if err != nil || retained != enrollment {
		t.Fatalf("invalid confirmation changed pending enrollment: error=%v", err)
	}
}

func formTextareaValue(body, name string) string {
	for _, match := range regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`).FindAllStringSubmatch(body, -1) {
		if navigationAttribute(match[1], "name") == name {
			return html.UnescapeString(match[2])
		}
	}
	return ""
}

func formRequireCheckbox(t *testing.T, body, name, value string, wantChecked bool) {
	t.Helper()
	for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		if navigationAttribute(tag, "name") != name || navigationAttribute(tag, "value") != value {
			continue
		}
		checked := regexp.MustCompile(`\schecked(?:\s|=|>)`).MatchString(tag)
		if checked != wantChecked {
			t.Errorf("checkbox %s=%q checked=%t, want %t", name, value, checked, wantChecked)
		}
		return
	}
	t.Errorf("checkbox %s=%q missing", name, value)
}
