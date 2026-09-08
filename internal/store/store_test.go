package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOwnerAuthenticationAndSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOwner(ctx, "second", "another-secure-password"); err == nil {
		t.Fatal("second owner was accepted")
	}
	got, err := s.Authenticate(ctx, "operator", "a-secure-test-password")
	if err != nil || got.ID != owner.ID || got.Role != rbac.Owner {
		t.Fatalf("authenticate returned %#v, %v", got, err)
	}
	if _, err := s.Authenticate(ctx, "operator", "wrong password"); err == nil {
		t.Fatal("wrong password authenticated")
	}
	token, err := s.CreateSession(ctx, owner.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.ResolveSession(ctx, token)
	if err != nil || resolved.ID != owner.ID {
		t.Fatalf("resolve session returned %#v, %v", resolved, err)
	}
}

func TestCreateAndListSite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"})
	if err != nil {
		t.Fatal(err)
	}
	sites, err := s.ListSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Domain != "example.com" || sites[0].Status != "queued" || !sites[0].RedisEnabled || !sites[0].FastCGICacheEnabled {
		t.Fatalf("unexpected sites: %#v", sites)
	}
	job, claimed, err := s.ClaimNextJob(ctx)
	if err != nil || !claimed || job.ID != jobID {
		t.Fatalf("claim returned %#v, %v, %v", job, claimed, err)
	}
	if err := s.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	job, err = s.Job(ctx, jobID)
	if err != nil || job.Status != "succeeded" || job.Progress != 100 {
		t.Fatalf("finished job is %#v, %v", job, err)
	}
	site, err := s.Site(ctx, "example-com")
	if err != nil || site.Status != "active" {
		t.Fatalf("finished site is %#v, %v", site, err)
	}
}

func TestScopedUsersSeeOnlyAssignedSites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []model.Site{
		{ID: "first-site", Domain: "first.example.com", Kind: model.Static},
		{ID: "second-site", Domain: "second.example.com", Kind: model.Static},
	} {
		if _, err := s.CreateSite(ctx, owner, site); err != nil {
			t.Fatal(err)
		}
	}
	collaborator, err := s.CreateUser(ctx, owner, "helper", "a-secure-test-password", rbac.Collaborator, []string{"first-site"})
	if err != nil {
		t.Fatal(err)
	}
	sites, err := s.ListSitesForUser(ctx, collaborator)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].ID != "first-site" {
		t.Fatalf("scoped list leaked sites: %#v", sites)
	}
	if !s.UserCanSite(ctx, collaborator, "first-site", rbac.ManageFiles) {
		t.Fatal("assigned collaborator capability was denied")
	}
	if s.UserCanSite(ctx, collaborator, "second-site", rbac.ManageFiles) {
		t.Fatal("unassigned site capability was allowed")
	}
	customer, err := s.CreateUser(ctx, owner, "customer", "a-secure-test-password", rbac.Customer, []string{"first-site"})
	if err != nil {
		t.Fatal(err)
	}
	if s.UserCanSite(ctx, customer, "first-site", rbac.DeploySite) {
		t.Fatal("customer received collaborator deployment capability")
	}
}

func TestRecentJobsRespectSiteAssignments(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []model.Site{
		{ID: "visible-site", Domain: "visible.example.com", Kind: model.Static},
		{ID: "private-site", Domain: "private.example.com", Kind: model.Static},
	} {
		if _, err := s.CreateSite(ctx, owner, site); err != nil {
			t.Fatal(err)
		}
	}
	collaborator, err := s.CreateUser(ctx, owner, "helper", "a-secure-test-password", rbac.Collaborator, []string{"visible-site"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.RecentJobsForUser(ctx, collaborator, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].TargetID != "visible-site" || jobs[0].Initiator != "operator" {
		t.Fatalf("scoped activity leaked jobs: %#v", jobs)
	}
	allJobs, err := s.RecentJobsForUser(ctx, owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(allJobs) != 2 {
		t.Fatalf("owner activity=%#v", allJobs)
	}
}

func TestDisablingUserRevokesSessionsAndProtectsOwnership(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, owner, "helper", "a-secure-test-password", rbac.Collaborator, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateSession(ctx, user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(ctx, owner, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSession(ctx, token); err == nil {
		t.Fatal("disabled user retained an active session")
	}
	if _, err := s.Authenticate(ctx, "helper", "a-secure-test-password"); err == nil {
		t.Fatal("disabled user authenticated")
	}
	if err := s.SetUserDisabled(ctx, owner, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "helper", "a-secure-test-password"); err != nil {
		t.Fatalf("re-enabled user did not authenticate: %v", err)
	}
	if err := s.SetUserDisabled(ctx, owner, owner.ID, true); err == nil {
		t.Fatal("owner disabled own account")
	}
}

func TestUpdatingUserAccessReplacesRoleGrantsAndProtectsAdministrators(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []model.Site{
		{ID: "first-site", Domain: "first.example.com", Kind: model.Static},
		{ID: "second-site", Domain: "second.example.com", Kind: model.Static},
	} {
		if _, err := s.CreateSite(ctx, owner, site); err != nil {
			t.Fatal(err)
		}
	}
	user, err := s.CreateUser(ctx, owner, "helper", "a-secure-test-password", rbac.Collaborator, []string{"first-site"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUserAccess(ctx, owner, user.ID, rbac.Customer, []string{"second-site"}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.User(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != rbac.Customer || len(updated.SiteIDs) != 1 || updated.SiteIDs[0] != "second-site" {
		t.Fatalf("updated user=%#v", updated)
	}
	if s.UserCanSite(ctx, updated, "first-site", rbac.ViewSite) || !s.UserCanSite(ctx, updated, "second-site", rbac.ViewSite) {
		t.Fatal("site grants were not replaced")
	}
	administrator, err := s.CreateUser(ctx, owner, "server-admin", "a-secure-test-password", rbac.Administrator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUserAccess(ctx, administrator, user.ID, rbac.Administrator, nil); err == nil {
		t.Fatal("administrator elevated another user to administrator")
	}
	if err := s.UpdateUserAccess(ctx, owner, owner.ID, rbac.Customer, nil); err == nil {
		t.Fatal("owner changed their own access")
	}
}

func TestPasswordChangeRevokesSessionsAndKeepsNewCredential(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateSession(ctx, owner.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, owner, "a-different-secure-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSession(ctx, token); err == nil {
		t.Fatal("password change retained an existing session")
	}
	if _, err := s.Authenticate(ctx, owner.Username, "a-secure-test-password"); err == nil {
		t.Fatal("old password still authenticated")
	}
	if _, err := s.Authenticate(ctx, owner.Username, "a-different-secure-password"); err != nil {
		t.Fatalf("new password did not authenticate: %v", err)
	}
}

func TestEmailUsernameNormalizesAuthenticationAndRenameKeepsIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "Owner@Example.COM", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if owner.Username != "owner@example.com" {
		t.Fatalf("owner username = %q", owner.Username)
	}
	if authenticated, err := s.Authenticate(ctx, " OWNER@EXAMPLE.COM ", "a-secure-test-password"); err != nil || authenticated.ID != owner.ID {
		t.Fatalf("normalized email login failed: user=%#v err=%v", authenticated, err)
	}
	user, err := s.CreateUser(ctx, owner, "legacy-user", "another-secure-password", rbac.Collaborator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated, err := s.Authenticate(ctx, " LEGACY-USER ", "another-secure-password"); err != nil || authenticated.ID != user.ID {
		t.Fatalf("legacy username no longer authenticated: user=%#v err=%v", authenticated, err)
	}
	updated, err := s.ChangeUsername(ctx, owner, user.ID, "Person@Example.COM")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != user.ID || updated.Username != "person@example.com" {
		t.Fatalf("rename changed identity or normalization: %#v", updated)
	}
	if _, err := s.Authenticate(ctx, "legacy-user", "another-secure-password"); err == nil {
		t.Fatal("old username still authenticated")
	}
	if authenticated, err := s.Authenticate(ctx, "PERSON@example.com", "another-secure-password"); err != nil || authenticated.ID != user.ID {
		t.Fatalf("renamed email did not authenticate: user=%#v err=%v", authenticated, err)
	}
	if _, err := s.ChangeUsername(ctx, owner, user.ID, "OWNER@example.com"); err == nil {
		t.Fatal("case-insensitive duplicate email was accepted")
	}
}

func TestUsernameRenameAuthorizationProtectsPrivilegedAccounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "owner", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	administrator, err := s.CreateUser(ctx, owner, "administrator", "another-secure-password", rbac.Administrator, nil)
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := s.CreateUser(ctx, owner, "collaborator", "another-secure-password", rbac.Collaborator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeUsername(ctx, collaborator, administrator.ID, "renamed-admin"); err == nil {
		t.Fatal("collaborator renamed another account")
	}
	if _, err := s.ChangeUsername(ctx, administrator, owner.ID, "renamed-owner"); err == nil {
		t.Fatal("administrator renamed owner")
	}
	if _, err := s.ChangeUsername(ctx, administrator, collaborator.ID, "renamed-collaborator"); err != nil {
		t.Fatalf("administrator could not rename collaborator: %v", err)
	}
}
