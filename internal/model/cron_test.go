package model

import "testing"

func TestValidateCronExpression(t *testing.T) {
	valid := []string{"* * * * *", "*/15 0-23/2 1,15 * 1-5", "0 3 1 * 0"}
	for _, expression := range valid {
		if err := ValidateCronExpression(expression); err != nil {
			t.Errorf("%q rejected: %v", expression, err)
		}
	}
	invalid := []string{"* * * *", "@daily", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "1/2 * * * *", "*/0 * * * *", "1--2 * * * *", "*  * * * *"}
	for _, expression := range invalid {
		if err := ValidateCronExpression(expression); err == nil {
			t.Errorf("%q accepted", expression)
		}
	}
}

func TestValidateCronCommandRejectsTraversalAndMultilineArguments(t *testing.T) {
	for _, command := range [][]string{{"php", "artisan", "schedule:run"}, {"/usr/bin/php", "script.php"}} {
		if err := ValidateCronCommand(command); err != nil {
			t.Fatalf("safe vector rejected: %v", err)
		}
	}
	for _, command := range [][]string{{"../bin/task"}, {"php", "ok\nroot-command"}, {""}} {
		if err := ValidateCronCommand(command); err == nil {
			t.Fatalf("unsafe vector accepted: %#v", command)
		}
	}
}

func TestCronScheduleSummaryUsesUTCAndKnownPresets(t *testing.T) {
	for expression, want := range map[string]string{"* * * * *": "Every minute", "*/15 * * * *": "Every 15 minutes", "0 3 1 * *": "Monthly on day 1 at 03:00 UTC", "7 2 * * *": "7 2 * * * UTC"} {
		if got := CronScheduleSummary(expression); got != want {
			t.Errorf("%q = %q, want %q", expression, got, want)
		}
	}
}
