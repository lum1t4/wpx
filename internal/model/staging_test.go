package model

import "testing"

func TestValidateStagingSelection(t *testing.T) {
	valid := []StagingSelection{
		{Full: true},
		{Files: []string{"wp-content/themes/example/style.css"}},
		{Tables: []string{"wp_options"}},
	}
	for _, selection := range valid {
		if err := ValidateStagingSelection(selection); err != nil {
			t.Fatalf("valid selection %#v rejected: %v", selection, err)
		}
	}
	invalid := []StagingSelection{
		{},
		{Full: true, Tables: []string{"wp_options"}},
		{Files: []string{"../wp-config.php"}},
		{Files: []string{"wp-config.php"}},
		{Files: []string{"wp-content/mu-plugins/wpx-staging.php"}},
		{Tables: []string{"wp_options; DROP TABLE users"}},
		{Tables: []string{"wp_options", "wp_options"}},
	}
	for _, selection := range invalid {
		if err := ValidateStagingSelection(selection); err == nil {
			t.Fatalf("invalid selection %#v was accepted", selection)
		}
	}
}
