package model

import (
	"strings"
	"testing"
)

func TestDatabaseIdentifiersComeOnlyFromUUID(t *testing.T) {
	id := "01234567-89ab-4cde-8f01-23456789abcd"
	name, username, err := DatabaseIdentifiers(id)
	if err != nil || name != username || !strings.HasPrefix(name, "wpx_") || len(name) != 20 {
		t.Fatalf("name=%q username=%q err=%v", name, username, err)
	}
	database := Database{ID: id, SiteID: "legacy-site", Label: "Application", Name: name, Username: username, Password: "abcdefghijklmnopqrstuvwxyz123456"}
	if err := ValidateDatabase(database); err != nil {
		t.Fatal(err)
	}
	database.Name = "user_supplied"
	if err := ValidateDatabase(database); err == nil {
		t.Fatal("accepted a SQL identifier not derived from the database UUID")
	}
	if _, _, err := DatabaseIdentifiers("legacy-site"); err == nil {
		t.Fatal("accepted a legacy slug as a database UUID")
	}
}
