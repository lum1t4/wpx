package rbac

import "testing"

func TestOwnershipIsOwnerOnly(t *testing.T) {
	for _, role := range []Role{Administrator, Collaborator, Customer} {
		if Allows(role, ManageOwnership) {
			t.Fatalf("role %q unexpectedly manages ownership", role)
		}
	}
	if !Allows(Owner, ManageOwnership) {
		t.Fatal("owner cannot manage ownership")
	}
}

func TestCustomerHasNoMutationCapabilities(t *testing.T) {
	for _, capability := range []Capability{DeploySite, ManageFiles, ManageDNS, ManageServer} {
		if Allows(Customer, capability) {
			t.Fatalf("customer unexpectedly has %q", capability)
		}
	}
}
