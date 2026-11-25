package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	GitHubPAT       string
	AnthropicAPIKey string
	JiraEmail       string
	JiraAPIToken    string
	JiraBaseURL     string
	GitHubTeamSlug  string

	PRMaxAgeDays int

	Model      string
	StatePath  string
	EventsPath string
	DryRun     bool
	LogLevel   string

	SlackWebhookURL string
	JiraProjectKeys []string

	HTTPTimeout      time.Duration
	AnthropicTimeout time.Duration
}

var required = []string{
	"GITHUB_PAT",
	"ANTHROPIC_API_KEY",
	"JIRA_EMAIL",
	"JIRA_API_TOKEN",
	"JIRA_BASE_URL",
	"GITHUB_TEAM_SLUG",
}

func Load(loadDotenv bool) (Config, error) {
	if loadDotenv {
		_ = godotenv.Load()
	}

	dryRun := envBool("DRY_RUN")

	var missing []string
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if !dryRun && os.Getenv("SLACK_WEBHOOK_URL") == "" {
		missing = append(missing, "SLACK_WEBHOOK_URL")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	maxAge, err := envInt("PR_MAX_AGE_DAYS", 14)
	if err != nil {
		return Config{}, err
	}

	return Config{
		GitHubPAT:       os.Getenv("GITHUB_PAT"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		JiraEmail:       os.Getenv("JIRA_EMAIL"),
		JiraAPIToken:    os.Getenv("JIRA_API_TOKEN"),
		JiraBaseURL:     strings.TrimRight(os.Getenv("JIRA_BASE_URL"), "/"),
		GitHubTeamSlug:  os.Getenv("GITHUB_TEAM_SLUG"),

		PRMaxAgeDays: maxAge,

		Model:      envString("MODEL", "claude-opus-5-5"),
		StatePath:  envString("STATE_PATH", "./state.json"),
		EventsPath: envString("EVENTS_PATH", "./briefings.jsonl"),
		DryRun:     dryRun,
		LogLevel:   envString("LOG_LEVEL", "INFO"),

		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
		JiraProjectKeys: envList("JIRA_PROJECT_KEYS"),

		HTTPTimeout:      10 * time.Second,
		AnthropicTimeout: 5 * time.Minute,
	}, nil
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return n, nil
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func envList(key string) []string {
	var out []string
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if v := strings.ToUpper(strings.TrimSpace(part)); v != "" {
			out = append(out, v)
		}
	}
	return out
}
