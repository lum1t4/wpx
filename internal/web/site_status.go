package web

import "strings"

func siteStatusLabel(status string) string {
	switch status {
	case "domain_changing":
		return "Changing domain"
	case "domain_change_failed":
		return "Domain change needs recovery"
	case "deleting":
		return "Deleting"
	case "delete_failed":
		return "Deletion needs recovery"
	case "php_changing":
		return "Changing PHP"
	case "php_change_failed":
		return "PHP change failed"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}
