package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type CronSchedule struct {
	ID          string
	SiteID      string
	Name        string
	Expression  string
	Command     []string
	Enabled     bool
	ApplyStatus string
	ApplyError  string
	CreatedAt   string
	UpdatedAt   string
}

type WordPressCronSetting struct {
	SiteID      string
	Replaced    bool
	Expression  string
	ApplyStatus string
	ApplyError  string
}

var cronIDPattern = regexp.MustCompile(`^cron_[a-zA-Z0-9_-]{8,80}$`)
var cronExecutablePattern = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._+-]*|/(?:[A-Za-z0-9._+-]+/)*[A-Za-z0-9._+-]+)$`)

func ValidateCronSchedule(schedule CronSchedule) error {
	if !cronIDPattern.MatchString(schedule.ID) {
		return errors.New("invalid cron schedule identifier")
	}
	if err := ValidateSiteID(schedule.SiteID); err != nil {
		return err
	}
	schedule.Name = strings.TrimSpace(schedule.Name)
	if schedule.Name == "" || len(schedule.Name) > 80 {
		return errors.New("schedule name must be between 1 and 80 characters")
	}
	if err := ValidateCronExpression(schedule.Expression); err != nil {
		return err
	}
	if err := ValidateCronCommand(schedule.Command); err != nil {
		return err
	}
	if schedule.ApplyStatus != "" && schedule.ApplyStatus != "pending" && schedule.ApplyStatus != "applied" && schedule.ApplyStatus != "failed" {
		return errors.New("invalid cron apply status")
	}
	return nil
}

func ValidateCronCommand(command []string) error {
	if len(command) == 0 || len(command) > 64 {
		return errors.New("command must contain between 1 and 64 arguments")
	}
	if !cronExecutablePattern.MatchString(command[0]) || strings.Contains(command[0], "..") {
		return errors.New("executable must be a command name or an absolute path without traversal")
	}
	// Scheduled work is an application command, not an alternate command shell
	// or privilege-management surface. The host still runs every accepted vector
	// as the site's dedicated Unix account.
	forbidden := map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "fish": true, "sudo": true, "su": true, "runuser": true, "systemctl": true, "pkexec": true}
	if forbidden[filepath.Base(command[0])] {
		return errors.New("shells and privilege-management commands cannot be scheduled")
	}
	for _, value := range command {
		if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("command arguments must be non-empty, single-line values up to 1024 characters")
		}
	}
	return nil
}

func ValidateCronExpression(expression string) error {
	fields := strings.Fields(expression)
	if len(fields) != 5 || strings.Join(fields, " ") != strings.TrimSpace(expression) {
		return errors.New("schedule must contain exactly five cron fields")
	}
	bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if err := validateCronField(field, bounds[i][0], bounds[i][1]); err != nil {
			return fmt.Errorf("invalid cron field %d: %w", i+1, err)
		}
	}
	return nil
}

func validateCronField(field string, minimum, maximum int) error {
	if field == "" || len(field) > 100 {
		return errors.New("field is empty or too long")
	}
	for _, item := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(item, "/")
		if hasStep {
			step, err := strconv.Atoi(stepText)
			if err != nil || step < 1 || step > maximum-minimum+1 || strings.Contains(stepText, "/") {
				return errors.New("step is outside the allowed range")
			}
		}
		if base == "*" {
			continue
		}
		startText, endText, hasRange := strings.Cut(base, "-")
		start, err := strconv.Atoi(startText)
		if err != nil || start < minimum || start > maximum {
			return errors.New("value is outside the allowed range")
		}
		if hasRange {
			end, err := strconv.Atoi(endText)
			if err != nil || end < start || end > maximum || strings.Contains(endText, "-") {
				return errors.New("range is invalid")
			}
		} else if hasStep {
			return errors.New("a stepped value must use * or a range")
		}
	}
	return nil
}

func ValidateWordPressCronSetting(setting WordPressCronSetting) error {
	if err := ValidateSiteID(setting.SiteID); err != nil {
		return err
	}
	if setting.Expression == "" {
		return errors.New("WordPress cron schedule is required")
	}
	return ValidateCronExpression(setting.Expression)
}
