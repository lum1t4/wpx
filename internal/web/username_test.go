//go:build linux

package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/rbac"
)

func TestAccountUsernameChangeRequiresCurrentPassword(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{"username": {"Owner@Example.COM"}, "current_password": {"wrong-password"}}
	response := navigationRequest(t, server, owner, http.MethodPost, "/account/username", form)
	requireNavigationStatus(t, response, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "current password is incorrect") {
		t.Fatal("username change did not report password confirmation failure")
	}
	unchanged, err := server.store.User(context.Background(), owner.ID)
	if err != nil || unchanged.Username != owner.Username {
		t.Fatalf("failed confirmation changed username: user=%#v err=%v", unchanged, err)
	}

	form.Set("current_password", "navigation-test-password")
	response = navigationRequest(t, server, owner, http.MethodPost, "/account/username", form)
	requireNavigationStatus(t, response, http.StatusSeeOther)
	if response.Header().Get("Location") != "/account/security?username=saved" {
		t.Fatalf("rename redirect = %q", response.Header().Get("Location"))
	}
	updated, err := server.store.User(context.Background(), owner.ID)
	if err != nil || updated.ID != owner.ID || updated.Username != "owner@example.com" {
		t.Fatalf("account rename did not preserve identity: user=%#v err=%v", updated, err)
	}
	if authenticated, err := server.store.Authenticate(context.Background(), "OWNER@EXAMPLE.COM", "navigation-test-password"); err != nil || authenticated.ID != owner.ID {
		t.Fatalf("renamed email login failed: user=%#v err=%v", authenticated, err)
	}
}

func TestAdministratorCanRenameManagedUserWithoutChangingAccess(t *testing.T) {
	server, owner, _ := navigationServer(t)
	administrator, err := server.store.CreateUser(context.Background(), owner, "server-admin", "navigation-test-password", rbac.Administrator, nil)
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := server.store.CreateUser(context.Background(), owner, "developer", "navigation-test-password", rbac.Collaborator, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := navigationRequest(t, server, administrator, http.MethodPost, "/users/"+collaborator.ID+"/username", url.Values{"username": {"Developer@Example.com"}, "current_password": {"wrong-password"}})
	requireNavigationStatus(t, response, http.StatusBadRequest)
	unchanged, err := server.store.User(context.Background(), collaborator.ID)
	if err != nil || unchanged.Username != collaborator.Username {
		t.Fatalf("failed administrator confirmation changed username: user=%#v err=%v", unchanged, err)
	}
	response = navigationRequest(t, server, administrator, http.MethodPost, "/users/"+collaborator.ID+"/username", url.Values{"username": {"Developer@Example.com"}, "current_password": {"navigation-test-password"}})
	requireNavigationStatus(t, response, http.StatusSeeOther)
	updated, err := server.store.User(context.Background(), collaborator.ID)
	if err != nil || updated.ID != collaborator.ID || updated.Role != collaborator.Role || updated.Username != "developer@example.com" {
		t.Fatalf("managed rename changed access or identity: user=%#v err=%v", updated, err)
	}
	response = navigationRequest(t, server, administrator, http.MethodPost, "/users/"+owner.ID+"/username", url.Values{"username": {"other-owner"}})
	requireNavigationStatus(t, response, http.StatusForbidden)
}

func TestUserFormsOfferEmailNamesAndAccessibleRoleHelp(t *testing.T) {
	server, owner, _ := navigationServer(t)
	response := navigationRequest(t, server, owner, http.MethodGet, "/account/security", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `action="/account/username"`) || !strings.Contains(response.Body.String(), "Username or email") {
		t.Fatal("account profile does not expose sign-in name editing")
	}
	response = navigationRequest(t, server, owner, http.MethodGet, "/users", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if !strings.Contains(body, `data-i18n="role_permissions"`) || !strings.Contains(body, `role="tooltip"`) || !strings.Contains(body, "group-focus-within:visible") || strings.Contains(body, "manages assigned sites, including files, deployments") {
		t.Fatal("role guidance is not concise accessible disclosure content")
	}
}
