package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_PAT", "ghp_test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("JIRA_EMAIL", "el@example.com")
	t.Setenv("JIRA_API_TOKEN", "jira-token")
	t.Setenv("JIRA_BASE_URL", "https://example.atlassian.net/")
	t.Setenv("GITHUB_TEAM_SLUG", "my-team")
	t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/A/B/c")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"PRMaxAgeDays", cfg.PRMaxAgeDays, 14},
		{"Model", cfg.Model, "claude-opus-5-5"},
		{"StatePath", cfg.StatePath, "./state.json"},
		{"EventsPath", cfg.EventsPath, "./briefings.jsonl"},
		{"DryRun", cfg.DryRun, false},
		{"LogLevel", cfg.LogLevel, "INFO"},
		{"HTTPTimeout", cfg.HTTPTimeout, 10 * time.Second},
		{"AnthropicTimeout", cfg.AnthropicTimeout, 5 * time.Minute},
		{"JiraProjectKeys len", len(cfg.JiraProjectKeys), 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadStripsTrailingSlashFromJiraBaseURL(t *testing.T) {
	setRequired(t)

	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.JiraBaseURL != "https://example.atlassian.net" {
		t.Errorf("JiraBaseURL = %q, want no trailing slash", cfg.JiraBaseURL)
	}
}

func TestLoadMissingRequiredListsAllMissing(t *testing.T) {
	setRequired(t)
	t.Setenv("GITHUB_PAT", "")
	t.Setenv("JIRA_EMAIL", "")

	_, err := Load(false)
	if err == nil {
		t.Fatal("Load() returned nil error, want missing-vars error")
	}
	for _, want := range []string{"GITHUB_PAT", "JIRA_EMAIL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "GITHUB_TEAM_SLUG") {
		t.Errorf("error %q mentions a variable that was set", err)
	}
}

func TestLoadRejectsNonIntegerMaxAge(t *testing.T) {
	setRequired(t)
	t.Setenv("PR_MAX_AGE_DAYS", "")
	t.Setenv("PR_MAX_AGE_DAYS", "forever")

	_, err := Load(false)
	if err == nil {
		t.Fatal("Load() returned nil error, want parse error")
	}
	if !strings.Contains(err.Error(), "PR_MAX_AGE_DAYS") {
		t.Errorf("error %q does not name the offending variable", err)
	}
}

func TestLoadParsesDryRunTruthyValues(t *testing.T) {
	truthy := []string{"true", "TRUE", "  True  ", "1", "yes", "on"}
	for _, v := range truthy {
		t.Run("truthy/"+v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("DRY_RUN", v)
			cfg, err := Load(false)
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if !cfg.DryRun {
				t.Errorf("DRY_RUN=%q gave DryRun=false, want true", v)
			}
		})
	}

	falsy := []string{"false", "0", "no", "off", "", "banana"}
	for _, v := range falsy {
		t.Run("falsy/"+v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("DRY_RUN", v)
			cfg, err := Load(false)
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if cfg.DryRun {
				t.Errorf("DRY_RUN=%q gave DryRun=true, want false", v)
			}
		})
	}
}

func TestLoadRequiresSlackUnlessDryRun(t *testing.T) {
	setRequired(t)
	t.Setenv("SLACK_WEBHOOK_URL", "")
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "SLACK_WEBHOOK_URL") {
		t.Errorf("Load() error = %v, want one naming SLACK_WEBHOOK_URL", err)
	}
	t.Setenv("DRY_RUN", "true")
	if _, err := Load(false); err != nil {
		t.Errorf("Load() with DRY_RUN and no webhook returned %v, want nil", err)
	}
}

func TestLoadParsesJiraProjectKeys(t *testing.T) {
	setRequired(t)
	t.Setenv("JIRA_PROJECT_KEYS", " abc, ,Def ,")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if strings.Join(cfg.JiraProjectKeys, ",") != "ABC,DEF" {
		t.Errorf("JiraProjectKeys = %v, want [ABC DEF]", cfg.JiraProjectKeys)
	}
}

func TestLoadOverridesFromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("PR_MAX_AGE_DAYS", "7")
	t.Setenv("MODEL", "claude-sonnet-5-5")
	t.Setenv("STATE_PATH", "/tmp/s.json")
	t.Setenv("EVENTS_PATH", "/tmp/e.jsonl")
	t.Setenv("LOG_LEVEL", "DEBUG")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PRMaxAgeDays != 7 || cfg.Model != "claude-sonnet-5-5" || cfg.StatePath != "/tmp/s.json" ||
		cfg.EventsPath != "/tmp/e.jsonl" || cfg.LogLevel != "DEBUG" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}
