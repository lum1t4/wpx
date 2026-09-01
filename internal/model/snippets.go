package model

import (
	"errors"
	"regexp"
	"strings"
)

type SiteSnippets struct {
	Nginx string `json:"nginx"`
	PHP   string `json:"php"`
}

var nginxSnippetLines = []*regexp.Regexp{
	regexp.MustCompile(`^client_max_body_size [1-9][0-9]{0,3}[kKmMgG];$`),
	regexp.MustCompile(`^(?:client_body_timeout|fastcgi_read_timeout) [1-9][0-9]{0,3}[smh];$`),
	regexp.MustCompile(`^expires (?:off|max|epoch|[+-]?[0-9]{1,4}[smhd]);$`),
	regexp.MustCompile(`^add_header [A-Za-z][A-Za-z0-9-]{0,63} "[^"\r\n]{1,256}"(?: always)?;$`),
}

var phpSnippetLine = regexp.MustCompile(`^php_admin_(value|flag)\[([a-z_]+)\] = ([A-Za-z0-9,._:+-]+)$`)

func ValidateSiteSnippets(site Site, snippets SiteSnippets) error {
	if len(snippets.Nginx) > 8192 || len(snippets.PHP) > 8192 {
		return errors.New("configuration snippet exceeds 8 KiB")
	}
	for _, line := range snippetLines(snippets.Nginx) {
		valid := false
		for _, pattern := range nginxSnippetLines {
			valid = valid || pattern.MatchString(line)
		}
		if !valid {
			return errors.New("Nginx snippet contains an unsupported directive: " + line)
		}
	}
	if snippets.PHP != "" && site.Kind != WordPress && site.Kind != PHP {
		return errors.New("PHP snippets are available only for WordPress and PHP sites")
	}
	for _, line := range snippetLines(snippets.PHP) {
		match := phpSnippetLine.FindStringSubmatch(line)
		if match == nil || !validPHPSetting(match[1], match[2], match[3]) {
			return errors.New("PHP snippet contains an unsupported directive: " + line)
		}
	}
	return nil
}

func snippetLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func validPHPSetting(kind, name, value string) bool {
	switch name {
	case "memory_limit", "upload_max_filesize", "post_max_size":
		return kind == "value" && regexp.MustCompile(`^[1-9][0-9]{0,4}[KMG]$`).MatchString(value)
	case "max_execution_time", "max_input_time":
		return kind == "value" && regexp.MustCompile(`^(?:0|[1-9][0-9]{0,3})$`).MatchString(value)
	case "max_input_vars":
		return kind == "value" && regexp.MustCompile(`^[1-9][0-9]{2,5}$`).MatchString(value)
	case "display_errors", "log_errors":
		return kind == "flag" && (value == "on" || value == "off")
	default:
		return false
	}
}
