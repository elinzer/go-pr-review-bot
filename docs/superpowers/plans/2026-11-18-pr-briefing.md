# PR Briefing Bot (Go) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single static Go binary, run by launchd every 15 minutes, that posts a Slack briefing (ticket summary, PR summary, ticket-vs-PR, where to look) for each PR requesting review from the team, replacing the Python `pr-review-bot`.

**Architecture:** `cmd/pr-review-bot` wires small `internal/` packages: read-only GitHub and Jira clients feed a `poller.Pipeline` that sizes the input (`budget`), builds the prompt (`prompt`), makes one structured-output Anthropic call (`briefer` + `anthropicclient`), and hands the result to `poller.Runner`, which posts to Slack (`notify`), records state (`state`) and events (`events`), and sends the weekly cost digest (`digest`). Every package is built test-first against fakes; only `anthropicclient` touches the SDK.

**Tech Stack:** Go 1.26, `github.com/anthropics/anthropic-sdk-go/v2`, `github.com/joho/godotenv`, stdlib (`net/http`, `net/http/httptest`, `encoding/json`, `log/slog`, `embed`, `testing`).

**Spec:** `docs/superpowers/specs/2026-11-18-pr-briefing-design.md`

**Supersedes:** `docs/superpowers/plans/2026-07-29-go-port.md`. Task 5 below executes that plan's Task 9 as written; nothing else from it is executed.

**Python reference:** `../pr-review-bot` (read-only). Keep it running untouched until Task 16's cutover.

## Global Constraints

- Go 1.26 (`go.mod` already declares `go 1.26.3`). Module path `github.com/elinzer/go-pr-review-bot`.
- Dependencies: exactly `github.com/anthropics/anthropic-sdk-go/v2` and `github.com/joho/godotenv` plus the standard library. No GitHub SDK, test framework, assertion library, or logging library.
- The bot never writes to GitHub. Every GitHub call is a `GET`.
- Every outbound call takes a `context.Context` first; every HTTP client has a timeout (`HTTPTimeout` 10s, `AnthropicTimeout` 5m).
- Model default `claude-opus-5-5`; `effort: high`; `max_tokens: 16000`; structured output via JSON schema; server-side fallback beta with `fallbacks: "default"`. One model call per PR.
- Input budget 150,000 estimated tokens (bytes ÷ 4) per PR, diff counted first. Per-file full-content cap 2,000 lines. At most 6 "where to look" items.
- A PR whose diff alone exceeds the budget gets no model call: a short "too large to brief" Slack notice instead, and is marked done.
- State is marked briefed only after Slack accepts the post. A model failure or a Slack failure increments `parse_failed_count`; at 3, set `skipped_manual`.
- `state.json` stays readable by and compatible with the Python shape; new key `briefed_at`. Never share the file with the running Python bot.
- Events go to `EVENTS_PATH` (default `./briefings.jsonl`).
- No code comments unless the behavior is dangerous and non-obvious.
- Commit after every task with a conventional prefix (`feat:`, `test:`, `chore:`).

## Review Focus

1. **Slack block text over 3,000 characters** (a long `pr_summary` or `why`) makes Slack reject the whole post with a 400, so the PR fails three times and is silently skipped. Expected: each section's text is truncated to fit and the post succeeds. Test: Task 12 `TestBriefingBlocksTruncateLongSections`.
2. **Model text containing `<`, `>`, or `&`** (`List<T>`, `a && b`, `<nil>`) is read by Slack as link/markup syntax and mangles or drops content. Expected: escaped to `&lt;`, `&gt;`, `&amp;` everywhere model or PR text is rendered. Test: Task 12 `TestRenderEscapesSlackControlCharacters`.
3. **Corrupt `state.json`** (truncated write, hand edit). Silently starting empty would re-brief every open PR and flood Slack. Expected: `state.Load` returns an error and the run exits non-zero without posting. Test: Task 3 `TestLoadCorruptFileReturnsError`.
4. **Changed file paths with spaces, `#`, `?`, or non-ASCII** sent unescaped to the contents API fetch the wrong file or 404. Expected: each path segment is escaped, `/` separators kept. Test: Task 6 `TestGetFileContentEscapesPath`.
5. **The weekly digest across a daylight-saving change** fires an hour early or late, or twice. Expected: the Monday 09:00 boundary is built in local time with `time.Date`, so it is 09:00 wall-clock on both sides of the change. Test: Task 13 `TestBoundaryAcrossDST`.

---

## File Structure

```
go-pr-review-bot/
├── .env.example
├── .gitignore
├── README.md
├── com.elinzer.go-pr-review-bot.plist
├── cmd/pr-review-bot/main.go         # flags, slog, wiring, -pr one-off mode
└── internal/
    ├── models/models.go               # shared structs only
    ├── config/config.go               # env → Config
    ├── state/state.go                 # state.json, Python-compatible
    ├── events/events.go               # JSONL event log
    ├── ghclient/
    │   ├── client.go                  # transport, auth, retry (July Task 9)
    │   ├── search.go                  # team review-request search (July Task 9)
    │   ├── files.go                   # PR files, file contents
    │   └── pull.go                    # PR summary by URL
    ├── budget/budget.go               # token estimates, skip list, full-file selection
    ├── jira/client.go                 # key extraction, ticket fetch, ADF flattening
    ├── prompt/
    │   ├── prompt.go                  # user message + schema
    │   └── briefing_system.md         # //go:embed
    ├── briefer/briefer.go             # MessageCreator interface, parse, validate
    ├── anthropicclient/client.go      # SDK adapter
    ├── notify/
    │   ├── render.go                  # text + Block Kit rendering
    │   └── slack.go                   # webhook POST
    ├── digest/digest.go               # weekly cost/volume digest
    └── poller/
        ├── pipeline.go                # one PR → Result
        └── runner.go                  # RunOnce orchestration
```

---

### Task 1: Rework `models` for briefings

> **Go concept: delete freely, the compiler finds the fallout.** Removing `Comment`/`Review` is safe precisely because nothing outside `models` uses them yet. Later, a removed type shows up as a compile error at every call site, which is the refactoring safety net Python never gave you.

**Files:**
- Modify: `internal/models/models.go`
- Modify: `internal/models/models_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `PullRequestSummary` (unchanged: `URL, RepoFullName string; Number int; Title, HeadSHA, Body, Branch string`).
  - `FileChange{Path, PreviousPath, Status, Patch string; Additions, Deletions int; FullContent string}`. `Status` is GitHub's file status (`added`, `modified`, `removed`, `renamed`, …).
  - `PRContext{Summary PullRequestSummary; Files []FileChange; OtherChanges []string}` with `ChangedFilePaths() map[string]struct{}` (kept) and `Totals() (additions, deletions int)`.
  - `JiraContext{Key, Title, Description, Epic string; LinkedIssues []string}`.
  - `AlignmentStatus string` with constants `AlignmentMet = "met"`, `AlignmentGap = "gap"`, `AlignmentExtra = "extra"`, `AlignmentUnclear = "unclear"` and `Valid() bool`.
  - `Alignment{Status AlignmentStatus \`json:"status"\`; Note string \`json:"note"\`}`.
  - `LookItem{File string \`json:"file"\`; Line int \`json:"line"\`; Why string \`json:"why"\`}`.
  - `Briefing{TicketSummary \`json:"ticket_summary"\`; PRSummary \`json:"pr_summary"\`; TicketAlignment []Alignment \`json:"ticket_alignment"\`; WhereToLook []LookItem \`json:"where_to_look"\`}`.
  - `Usage{Model string \`json:"model"\`; InputTokens int64 \`json:"input_tokens"\`; OutputTokens int64 \`json:"output_tokens"\`}`.
  - Removed: `Evidence`, `Severity`, `Comment`, `Review`.

- [ ] **Step 1: Replace the tests**

Keep `TestChangedFilePathsDeduplicates` and `TestChangedFilePathsEmpty`; delete `TestSeverityValid`; add:

```go
func TestTotalsSumsFiles(t *testing.T) {
	ctx := PRContext{Files: []FileChange{{Additions: 3, Deletions: 1}, {Additions: 10, Deletions: 4}}}
	add, del := ctx.Totals()
	if add != 13 || del != 5 {
		t.Errorf("Totals() = (%d, %d), want (13, 5)", add, del)
	}
}

func TestAlignmentStatusValid(t *testing.T) {
	for _, s := range []AlignmentStatus{AlignmentMet, AlignmentGap, AlignmentExtra, AlignmentUnclear} {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	for _, s := range []AlignmentStatus{"", "done", "MET"} {
		if s.Valid() {
			t.Errorf("%q.Valid() = true, want false", s)
		}
	}
}

func TestBriefingJSONTags(t *testing.T) {
	raw := `{"ticket_summary":"t","pr_summary":"p","ticket_alignment":[{"status":"gap","note":"n"}],"where_to_look":[{"file":"a.go","line":4,"why":"w"}]}`
	var b Briefing
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if b.TicketSummary != "t" || b.PRSummary != "p" || b.TicketAlignment[0].Status != AlignmentGap || b.WhereToLook[0].Line != 4 {
		t.Errorf("decoded %+v", b)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/models/`
Expected: FAIL to compile (`ctx.Totals undefined`, `undefined: AlignmentStatus`, `undefined: Briefing`).

- [ ] **Step 3: Implement the types listed under Interfaces in `internal/models/models.go`**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/models/ -v && go vet ./...`
Expected: PASS, five tests; vet silent.

- [ ] **Step 5: Commit**

```bash
git add internal/models/
git commit -m "feat: rework models for PR briefings"
```

---

### Task 2: Finish `config`

> **Note:** `internal/config/config.go` and `config_test.go` exist uncommitted from the July plan. The test file targets the old `Config`; replace it.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config{GitHubPAT, AnthropicAPIKey, JiraEmail, JiraAPIToken, JiraBaseURL, GitHubTeamSlug string; PRMaxAgeDays int; Model, StatePath, EventsPath string; DryRun bool; LogLevel, SlackWebhookURL string; JiraProjectKeys []string; HTTPTimeout, AnthropicTimeout time.Duration}` and `config.Load(loadDotenv bool) (Config, error)`. No `PollIntervalSeconds`.

Rules: required vars `GITHUB_PAT, ANTHROPIC_API_KEY, JIRA_EMAIL, JIRA_API_TOKEN, JIRA_BASE_URL, GITHUB_TEAM_SLUG`, plus `SLACK_WEBHOOK_URL` unless dry run; the error lists every missing var. Defaults: `PR_MAX_AGE_DAYS` 14, `MODEL` `claude-opus-5-5`, `STATE_PATH` `./state.json`, `EVENTS_PATH` `./briefings.jsonl`, `LOG_LEVEL` `INFO`, timeouts 10s / 5m. `DRY_RUN` truthy set: `true, 1, yes, on` (trimmed, case-insensitive). `JIRA_PROJECT_KEYS` comma-split, trimmed, uppercased, empties dropped. `JIRA_BASE_URL` trailing `/` stripped.

- [ ] **Step 1: Rewrite the tests**

Keep `setRequired` but add `t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/A/B/c")`. Keep `TestLoadStripsTrailingSlashFromJiraBaseURL`, `TestLoadMissingRequiredListsAllMissing`, `TestLoadRejectsNonIntegerMaxAge`, `TestLoadParsesDryRunTruthyValues` as they are. Delete `TestLoadRejectsNonIntegerPollInterval`. Replace `TestLoadDefaults` and `TestLoadOverridesFromEnv`, and add:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/`
Expected: FAIL to compile (`undefined: Load`, `cfg.EventsPath undefined`).

- [ ] **Step 3: Implement `Config` and `Load` in `internal/config/config.go`**

Use `godotenv.Load()` (error ignored: a missing `.env` is normal under launchd) when `loadDotenv` is true. The July plan's `envString`/`envInt`/`envBool` helpers fit as-is.

- [ ] **Step 4: Tidy modules and run**

Run: `go mod tidy && go test ./internal/config/ -v`
Expected: PASS; `go.mod` lists `github.com/joho/godotenv` as a direct requirement (no `// indirect`).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/config/
git commit -m "feat: add config package for briefing bot"
```

---

### Task 3: `state` package

> **Go concept: pointers signal absence.** `review_id` can legally be any integer, so "never set" can't be the zero value: it's `*int64`, `nil` meaning absent. Timestamps use plain `string` because `""` is never a legal timestamp. `omitempty` then keeps unset keys out of the file, so it still looks like Python's.
>
> **Deliberate deviation from Python:** a corrupt file is an error, not an empty start. Empty state means "brief every open PR again," which floods Slack.

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: `models.PullRequestSummary`.
- Produces: `state.Load(path string) (*State, error)` (missing file → empty state, nil error; unreadable or invalid JSON → error). `*State` methods: `IsDone(url string) bool`, `Pending(prs []models.PullRequestSummary) []models.PullRequestSummary`, `MarkBriefed(url string) error`, `MarkDryRun(url string) error`, `IncrementFailed(url string) (int, error)`, `MarkSkippedManual(url string) error`. Exported field `Now func() time.Time` (default `time.Now`).

Entry JSON keys: `review_id` (`*int64`), `reviewed_at`, `briefed_at`, `dry_run_logged_at` (strings, RFC 3339 UTC), `parse_failed_count` (int), `skipped_manual` (bool), all `omitempty`. Top level `{"reviews": {url: entry}}`. `IsDone` is true when any of `review_id`, `dry_run_logged_at`, `briefed_at` is set or `skipped_manual` is true. Every mutation saves atomically (write `path + ".tmp"`, then `os.Rename`), creating the parent directory if needed.

- [ ] **Step 1: Write the failing tests**

```go
func newTestState(t *testing.T) (*State, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	return s, path
}

func TestLoadMissingFileStartsEmpty(t *testing.T)
// IsDone("https://github.com/o/r/pull/1") == false

func TestLoadCorruptFileReturnsError(t *testing.T)
// write "{not json" to path; Load(path) returns non-nil error

func TestMarkBriefedPersistsAndWritesBriefedAt(t *testing.T)
// MarkBriefed(url); IsDone true; reload from disk IsDone true;
// raw JSON reviews[url]["briefed_at"] == "2026-11-18T12:00:00Z";
// keys "review_id", "skipped_manual", "parse_failed_count" absent

func TestMarkDryRunCountsAsDone(t *testing.T)

func TestIncrementFailedAccumulatesWithoutMarkingDone(t *testing.T)
// three calls return 1, 2, 3; IsDone stays false

func TestMarkSkippedManualIsDone(t *testing.T)

func TestPendingFiltersDone(t *testing.T)
// three PRs; MarkBriefed #1, MarkSkippedManual #2; Pending returns only #3

func TestReadsAndPreservesPythonEntries(t *testing.T)
// seed:
// {"reviews":{"https://github.com/o/r/pull/42":{"review_id":999,"reviewed_at":"2026-06-19T17:37:00+00:00"},
//             "https://github.com/o/r/pull/43":{"skipped_manual":true,"parse_failed_count":3},
//             "https://github.com/o/r/pull/44":{"dry_run_logged_at":"2026-09-30T10:00:00+00:00"}}}
// all three IsDone; then MarkBriefed(".../pull/45"); raw JSON still has
// reviews[pull/42]["review_id"] == 999 and ["reviewed_at"] == "2026-06-19T17:37:00+00:00"
```

Write each body with `t.Errorf`/`t.Fatalf` on the assertions in its comment.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/state/`
Expected: FAIL to compile (`undefined: Load`).

- [ ] **Step 3: Implement `internal/state/state.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/state/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Check the real Python file has no keys the struct would drop**

```bash
python3 -c "import json,collections;d=json.load(open('../pr-review-bot/state.json'));print(collections.Counter(k for e in d['reviews'].values() for k in e))"
```
Expected: only `review_id`, `reviewed_at`, `parse_failed_count`, `dry_run_logged_at`, `skipped_manual`. Any other key: add it to the entry struct with a matching tag before committing.

- [ ] **Step 6: Commit**

```bash
git add internal/state/
git commit -m "feat: add Python-compatible state with briefed_at"
```

---

### Task 4: `events` package

> **Go concept: `omitzero` (Go 1.24+).** `omitempty` never omits a struct, so a zero `time.Time` would serialize as `"0001-01-01T00:00:00Z"`. `omitzero` omits any value equal to its type's zero value, structs included. Use it on the time fields.

**Files:**
- Create: `internal/events/events.go`
- Test: `internal/events/events_test.go`

**Interfaces:**
- Consumes: `models.Usage`.
- Produces:
  - Constants `TypeBriefingCreated = "briefing_created"`, `TypeBriefingSkippedLarge = "briefing_skipped_large"`, `TypeBriefingFailed = "briefing_failed"`, `TypeDigestSent = "digest_sent"`.
  - `Event{Type string \`json:"type"\`; TS time.Time \`json:"ts"\`; PRURL \`pr_url\`; Repo \`repo\`; Number int \`number\`; Title \`title\`; Model \`model\`; JiraKey \`jira_key\`; Usage []models.Usage \`usage\`; FullFiles []string \`full_files\`; DiffOnlyFiles []string \`diff_only_files\`; BudgetExhausted bool \`budget_exhausted\`; DryRun bool \`dry_run\`; Reason string \`reason\`; EstimatedTokens int \`estimated_tokens\`; Files int \`files\`; PeriodStart, PeriodEnd time.Time \`period_start\`/\`period_end\`}`. Every field except `type` and `ts` is `omitempty` (`omitzero` for the two period times).
  - `events.Open(path string) *Log`; `*Log` has exported `Now func() time.Time` (default `time.Now`), `Append(e Event) error` (sets `TS = Now().UTC()`, appends one JSON line, creates parent dir), `Read() ([]Event, int, error)` (missing file → `nil, 0, nil`; returns the count of unparseable or type-less lines as the second value).

- [ ] **Step 1: Write the failing tests**

```go
func TestAppendThenRead(t *testing.T)
// Open(tmp/briefings.jsonl); Now fixed 2026-11-18T12:00:00Z;
// Append(Event{Type: TypeBriefingCreated, PRURL: "u", Usage: []models.Usage{{Model: "claude-opus-5-5", InputTokens: 100, OutputTokens: 20}}});
// Read returns 1 event, 0 unreadable; Type, PRURL, Usage[0].InputTokens == 100, TS equal to fixed time

func TestAppendOmitsUnsetFields(t *testing.T)
// after Append(Event{Type: TypeDigestSent, PeriodEnd: fixed}), raw line has no "pr_url", "usage", "period_start", "dry_run" keys and has "period_end"

func TestReadMissingFile(t *testing.T)
// Read on nonexistent path → nil slice, 0, nil error

func TestReadCountsUnreadableLines(t *testing.T)
// file contents: valid line, "{broken", "", `{"ts":"2026-11-18T12:00:00Z"}` (no type), valid line
// → 2 events, 2 unreadable
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/events/`
Expected: FAIL to compile (`undefined: Open`).

- [ ] **Step 3: Implement `internal/events/events.go` per Interfaces**

Read line by line with `bufio.Scanner`; raise its buffer to 1 MB with `scanner.Buffer` since one event can carry long file lists.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/events/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/events/
git commit -m "feat: add JSONL event log"
```

---

### Task 5: GitHub transport and team search

**Files:** as listed in Task 9 of `docs/superpowers/plans/2026-07-29-go-port.md` (`internal/ghclient/client.go`, `search.go`, `search_test.go`).

**Interfaces:**
- Consumes: `models.PullRequestSummary`.
- Produces (from July Task 9): `ghclient.New(pat, teamSlug string, maxAgeDays int, timeout time.Duration) *Client` with exported `BaseURL` and `RetryDelay`; `(*Client).ListTeamReviewRequests(ctx) ([]models.PullRequestSummary, error)`; unexported `do`, `doWithRetry`, `APIError` with `Retryable() bool`, `searchQuery`, `repoFromURL`.

- [ ] **Step 1: Execute July plan Task 9 Steps 1–5 exactly as written**

The search query must stay `is:pr is:open -is:draft team-review-requested:<slug> created:>=<YYYY-MM-DD>`, matching `../pr-review-bot/pr_review/github_client.py`.

- [ ] **Step 2: Run**

Run: `go test ./internal/ghclient/ -v && go vet ./...`
Expected: PASS, including the retry-once-on-5xx and no-retry-on-401 tests.

- [ ] **Step 3: Commit**

```bash
git add internal/ghclient/
git commit -m "feat: add GitHub transport with retry and team review-request search"
```

---

### Task 6: PR files, file contents, and PR-by-URL

> **Go concept: `url.PathEscape` per segment.** Escaping the whole path would turn `/` into `%2F` and GitHub would 404. Split on `/`, escape each segment, join.

**Files:**
- Modify: `internal/ghclient/client.go` (add a raw-body request helper)
- Create: `internal/ghclient/files.go`, `internal/ghclient/pull.go`
- Test: `internal/ghclient/files_test.go`, `internal/ghclient/pull_test.go`

**Interfaces:**
- Consumes: Task 5's `Client`, `doWithRetry`, `APIError`.
- Produces:
  - `(*Client).GetPRContext(ctx, summary models.PullRequestSummary) (models.PRContext, error)`: pages `GET /repos/{repo}/pulls/{n}/files?per_page=100&page=N` until a short page. A file with a non-empty `patch` becomes a `FileChange` (`PreviousPath` set only when `status == "renamed"`). A file without a patch becomes an `OtherChanges` string: `renamed: {previous} -> {path} (no content change)` when renamed, else `{status}: {path} (no diff shown: binary or too large)`.
  - `(*Client).GetFileContent(ctx, repo, path, ref string) (string, error)`: `GET /repos/{repo}/contents/{escaped path}?ref={url.QueryEscape(ref)}` with `Accept: application/vnd.github.raw+json`; returns the body as a string. Same retry rules as JSON calls.
  - `(*Client).GetPullRequest(ctx, prURL string) (models.PullRequestSummary, error)`: accepts `https://github.com/{owner}/{repo}/pull/{n}` (trailing `/files` or `/` tolerated), `GET /repos/{owner}/{repo}/pulls/{n}`, maps `html_url, title, body, head.sha, head.ref, number` and repo full name. Malformed URL → error naming the URL, no HTTP call.

- [ ] **Step 1: Write the failing tests**

Use `httptest.NewServer`, set `c.BaseURL = srv.URL`, `c.RetryDelay = 0`.

```go
func TestGetPRContextPaginatesAndClassifies(t *testing.T)
// page 1: 100 files each {"filename":"f{i}.go","status":"modified","patch":"@@ -1 +1 @@\n+x","additions":1,"deletions":0}
// page 2: [{"filename":"new.go","previous_filename":"old.go","status":"renamed","patch":"@@ -1 +1 @@\n+y","additions":1,"deletions":1},
//          {"filename":"moved.go","previous_filename":"was.go","status":"renamed"},
//          {"filename":"logo.png","status":"added"}]
// → len(Files) == 101; Files[100].PreviousPath == "old.go" and Status == "renamed";
//   OtherChanges == ["renamed: was.go -> moved.go (no content change)",
//                    "added: logo.png (no diff shown: binary or too large)"];
//   server saw page=1 and page=2 and nothing else

func TestGetFileContentSendsRawAcceptAndRef(t *testing.T)
// handler asserts r.Header.Get("Accept") == "application/vnd.github.raw+json",
// r.URL.Query().Get("ref") == "abc123", path == "/repos/o/r/contents/pkg/a.go"; writes "package a\n"
// → returns "package a\n"

func TestGetFileContentEscapesPath(t *testing.T)
// GetFileContent(ctx, "o/r", "docs/my file#1.md", "sha")
// handler asserts r.URL.EscapedPath() == "/repos/o/r/contents/docs/my%20file%231.md"

func TestGetFileContentReturnsAPIErrorOn404(t *testing.T)
// 404 → error; errors.As(err, &apiErr) and apiErr.Status == 404; one request only

func TestGetPullRequestParsesURL(t *testing.T)
// GetPullRequest(ctx, "https://github.com/o/r/pull/42/files") hits /repos/o/r/pulls/42;
// returned RepoFullName "o/r", Number 42, HeadSHA and Branch from head.sha / head.ref

func TestGetPullRequestRejectsMalformedURL(t *testing.T)
// "https://github.com/o/r/issues/42" → error, zero requests to the server
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ghclient/`
Expected: FAIL to compile (`c.GetPRContext undefined`).

- [ ] **Step 3: Implement per Interfaces**

Add an unexported `doRaw(ctx, path, accept string) (string, error)` to `client.go` sharing auth, `APIError` construction and the retry rule with `do`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/ghclient/ -v`
Expected: PASS, all Task 5 and Task 6 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/ghclient/
git commit -m "feat: fetch PR files, file contents, and PRs by URL"
```

---

### Task 7: `budget` package

> **Go concept: `sort.SliceStable` with a tiebreak.** Sorting by size alone would make which file gets dropped at the budget edge depend on map iteration order, which Go randomizes on purpose. Sort by size, then path, so the same PR always gets the same selection.

**Files:**
- Create: `internal/budget/budget.go`
- Test: `internal/budget/budget_test.go`

**Interfaces:**
- Consumes: `models.PRContext`, `models.FileChange`.
- Produces:
  - `const TokenBudget = 150_000`, `const MaxFileLines = 2000`.
  - `EstimateTokens(s string) int` → `(len(s) + 3) / 4`.
  - `Skipped(path string) bool`: true for base names `go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `poetry.lock`, `Pipfile.lock`, `Cargo.lock`, `Gemfile.lock`; any path segment equal to `vendor`, `node_modules`, `__snapshots__`; suffixes `.min.js`, `.min.css`, `.map`, `.snap`, `.pb.go`, `_gen.go`; any segment containing `generated` (case-insensitive).
  - `DiffTokens(pr models.PRContext) int`: sum of `EstimateTokens` over every `Patch` and every `OtherChanges` line.
  - `Eligible(pr models.PRContext) []string`: paths of `Files` that are not `Skipped` and whose `Status != "removed"`, in PR order.
  - `Selection{Full []string; DiffOnly []string; BudgetExhausted bool}`.
  - `Select(diffTokens int, contents map[string]string, budget int) Selection`.

`Select` algorithm (the spec fixes these rules):

```
remaining := budget - diffTokens
for each path in contents:
    if lineCount(content) > MaxFileLines → DiffOnly
    else candidates += (path, EstimateTokens(content))
sort candidates by tokens asc, then path asc
for each candidate in order:
    if exhausted or tokens > remaining → DiffOnly, exhausted = true
    else Full, remaining -= tokens
BudgetExhausted = exhausted
sort Full and DiffOnly by path
```

`lineCount` is `strings.Count(s, "\n")`, plus 1 if `s` is non-empty and doesn't end in `"\n"`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSkipped(t *testing.T)
// table: true for "go.sum", "web/package-lock.json", "vendor/x/y.go", "a/node_modules/b.js",
// "app.min.js", "dist/app.js.map", "ui/__snapshots__/x.snap", "api/v1/svc.pb.go", "mock_gen.go",
// "zz_generated.deepcopy.go", "Generated/Foo.cs";
// false for "main.go", "vendored.go", "docs/generator.md", "maps.go", "snapshot.go"

func TestEligibleExcludesSkippedAndRemoved(t *testing.T)
// Files: a.go modified, go.sum modified, gone.go removed, b.go added → ["a.go", "b.go"]

func TestDiffTokensCountsPatchesAndOtherChanges(t *testing.T)
// patch of 400 bytes + other change line of 40 bytes → 110

func TestSelectIncludesAllWhenUnderBudget(t *testing.T)
// contents a.go (40 bytes), b.go (80 bytes); Select(0, contents, 1000)
// → Full ["a.go","b.go"], DiffOnly [], BudgetExhausted false

func TestSelectDropsFilesOverLineCap(t *testing.T)
// big.go = strings.Repeat("x\n", 2001); small.go 10 bytes; Select(0, c, 1_000_000)
// → Full ["small.go"], DiffOnly ["big.go"], BudgetExhausted false

func TestSelectAllowsExactlyMaxLines(t *testing.T)
// strings.Repeat("x\n", 2000) → Full

func TestSelectFillsSmallestFirstAndStopsAtBudget(t *testing.T)
// a.go 400 bytes (100 tok), b.go 800 bytes (200 tok), c.go 1200 bytes (300 tok); Select(50, c, 400)
// remaining 350 → a (100) fits, b (200) fits → 50 left, c doesn't
// → Full ["a.go","b.go"], DiffOnly ["c.go"], BudgetExhausted true

func TestSelectDiffAlreadyOverBudget(t *testing.T)
// Select(500, {"a.go": 4 bytes}, 400) → Full [], DiffOnly ["a.go"], BudgetExhausted true
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/budget/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/budget/budget.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/budget/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/budget/
git commit -m "feat: add input budget and full-file selection"
```

---

### Task 8: `jira` client

> **Go concept: decoding unknown JSON into `any`.** ADF is a recursive tree whose node shapes vary, so declaring structs for it is busywork. Decode `description` into `any` and walk it with type switches on `map[string]any`, `[]any` and `string`. JSON numbers decode as `float64`, which matters for `attrs.order` and `attrs.level`.

**Files:**
- Create: `internal/jira/client.go`
- Test: `internal/jira/client_test.go`
- Reference: `../pr-review-bot/pr_review/jira_client.py`, `../pr-review-bot/tests/test_jira_client.py`

**Interfaces:**
- Consumes: `models.JiraContext`.
- Produces:
  - `jira.ExtractKey(branch, body string, projectKeys []string) string`: first `\b([A-Z]{2,}-\d+)\b` match in `branch`; otherwise the first match in `body` whose project prefix is in `projectKeys` (any match if `projectKeys` is empty); `""` when none.
  - `jira.New(baseURL, email, apiToken string, timeout time.Duration) *Client` with exported `BaseURL`.
  - `(*Client).FetchTicket(ctx, key string) (*models.JiraContext, error)`: `GET {BaseURL}/rest/api/3/issue/{key}?fields=summary,description,parent,issuelinks` with basic auth and `Accept: application/json`. Non-200, transport and decode failures return `nil` and an error that names the key and status. On 401/403, also `slog.Warn` "Jira auth failed … check JIRA_API_TOKEN".
  - `Epic` = `"{parent.key}: {parent.fields.summary}"` when `parent` is present. `LinkedIssues` = `"{relation} {key}: {summary}"` per link, `relation` from `type.outward` for `outwardIssue` and `type.inward` for `inwardIssue` (default `relates to`). `Description` = flattened ADF, trimmed.
  - Unexported `adfToText(node any, depth int) string` and `listToText(node map[string]any, depth int) string`, matching Python's `_adf_to_text` / `_list_to_text` node by node (text, hardBreak, inline/block/embed cards → url, mention/emoji → attrs.text, bullet/ordered lists with nesting and `attrs.order`, heading → `#`×level, paragraph/codeBlock/blockquote/panel/tableRow → trailing blank line).

- [ ] **Step 1: Translate every test in `../pr-review-bot/tests/test_jira_client.py` into `client_test.go`**

Use `httptest.NewServer` for fetch tests and table tests for `ExtractKey`/`adfToText`. Keep each Python test's name in Go form (`test_extract_key_prefers_branch` → `TestExtractKeyPrefersBranch`) and its inputs and expected strings byte-for-byte. Python's "returns None" cases become "returns nil and a non-nil error".

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jira/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/jira/client.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/jira/ -v`
Expected: PASS, one Go test (or subtest) per Python test.

- [ ] **Step 5: Commit**

```bash
git add internal/jira/
git commit -m "feat: add Jira client with epic and linked-issue context"
```

---

### Task 9: `prompt` package

> **Go concept: `//go:embed`.** The system prompt lives in a real `.md` file the compiler bakes into the binary. Editing the prompt is editing a text file, not escaping a Go string, and there's still only one file to deploy.

**Files:**
- Create: `internal/prompt/briefing_system.md`, `internal/prompt/prompt.go`
- Test: `internal/prompt/prompt_test.go`

**Interfaces:**
- Consumes: `models.PRContext`, `models.FileChange` (incl. `FullContent`), `models.JiraContext`.
- Produces: `prompt.System string` (embedded), `prompt.Schema() map[string]any`, `prompt.BuildUser(pr models.PRContext, jira *models.JiraContext, jiraKey string) string`.

`BuildUser` layout:

```
# Pull request

Repo: {repo}
Title: {title}
Branch: {branch}

## PR body
{body or "(empty)"}

## Jira context
{one of:}
  Jira ticket: {key}\nTitle: {title}\n[Epic: {epic}\n]Description:\n{description or "(empty)"}[\nLinked issues:\n- {issue}...]
  Ticket {jiraKey} is referenced but could not be fetched.
  No linked Jira ticket found.

## Files
{for each file:}
=== FILE: {path}[ (renamed from {previous})] (+{a} -{d}) [full file + diff] ===
--- full file at PR head ---
{full content}
--- diff ---
{patch}

=== FILE: {path} (+{a} -{d}) [diff only] ===
{patch}

## Files changed without a diff        (only when OtherChanges is non-empty)
- {line}
```

`Schema()` returns a JSON schema with `additionalProperties: false` and every property `required` at every level: `ticket_summary` string, `pr_summary` string, `ticket_alignment` array of `{status: enum [met, gap, extra, unclear], note: string}`, `where_to_look` array of `{file: string, line: integer, why: string}`.

- [ ] **Step 1: Write `internal/prompt/briefing_system.md` with exactly this text**

```markdown
You are briefing a senior engineer who is about to review a pull request. Give them a head start; do not review it for them. They will read the diff themselves and write every review comment.

You receive the PR's metadata and description, the linked Jira ticket when there is one, and each changed file's diff. Files marked [full file + diff] also include the complete file at the PR's head commit. Files marked [diff only] do not, so you have not seen the rest of those files.

Fill in:

- ticket_summary: 1-3 sentences on what the ticket asks for and why, in plain words. Mention the epic or linked issues only when they change what the work means. Empty string when there is no ticket.
- pr_summary: 2-4 sentences on what the PR changes and how. Describe behavior, not a file-by-file list.
- ticket_alignment: how the PR lines up with the ticket. "met" for requirements the PR clearly addresses, "gap" for requirements it appears not to address, "extra" for changes the ticket does not ask for, "unclear" when you cannot tell from what you were given. Empty array when there is no ticket.
- where_to_look: up to 6 places that most deserve the reviewer's attention, most important first. Each names a file from this PR, a line number in the new version of that file (0 when the concern is the whole file), and 1-2 sentences on what could go wrong and what to check.

For where_to_look:

- Prioritize risk: changed behavior, edge cases and boundary values, error handling, concurrency, data and schema migrations, security and permissions, and callers affected by a changed signature or return value.
- When a concern depends on code you were not shown, phrase it as something to verify ("check that callers of Parse handle the new nil return"), not as a defect.
- Skip style, naming and formatting.
- Do not restate what the diff plainly does.
- Fewer, sharper items beat a long list. If little stands out, return fewer items.

The ticket describes intent. It is not evidence of what the code does.
```

- [ ] **Step 2: Write the failing tests**

```go
func TestSystemPromptEmbedded(t *testing.T)
// strings.Contains(System, "Give them a head start") and "[diff only]"

func TestBuildUserLabelsFullAndDiffOnlyFiles(t *testing.T)
// Files: a.go with FullContent "package a\nfunc A() {}\n" and Patch "@@ -1 +1 @@\n+func A() {}";
//        b.go with Patch only
// User contains "=== FILE: a.go (+1 -0) [full file + diff] ===\n--- full file at PR head ---\npackage a",
//               "--- diff ---\n@@ -1 +1 @@", "=== FILE: b.go (+1 -0) [diff only] ==="

func TestBuildUserRenamedAndOtherChanges(t *testing.T)
// PreviousPath "old.go" → contains "=== FILE: new.go (renamed from old.go) ";
// OtherChanges ["added: logo.png (no diff shown: binary or too large)"] →
// contains "## Files changed without a diff\n- added: logo.png"

func TestBuildUserJiraVariants(t *testing.T)
// jira non-nil with Epic and LinkedIssues → contains "Jira ticket: ABC-1", "Epic: ABC-0: Parent", "Linked issues:\n- blocks ABC-2: Other"
// jira nil, key "ABC-1" → contains "Ticket ABC-1 is referenced but could not be fetched."
// jira nil, key "" → contains "No linked Jira ticket found."

func TestBuildUserEmptyBody(t *testing.T)
// contains "## PR body\n(empty)"

func TestSchemaShape(t *testing.T)
// json.Marshal(Schema()) succeeds; decoded: top-level "required" has all four keys,
// "additionalProperties" == false; where_to_look.items.properties.line.type == "integer";
// ticket_alignment.items.properties.status.enum == ["met","gap","extra","unclear"]
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/prompt/`
Expected: FAIL to compile.

- [ ] **Step 4: Implement `internal/prompt/prompt.go` per Interfaces using `strings.Builder`**

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/prompt/ -v`
Expected: PASS, six tests.

- [ ] **Step 6: Commit**

```bash
git add internal/prompt/
git commit -m "feat: add briefing prompt and output schema"
```

---

### Task 10: `briefer` package

> **Go concept: a custom error type that carries data.** A refused or truncated call still cost tokens, and the event log needs that usage. Returning `(Briefing, []Usage, error)` loses it on the error path by convention; a `*briefer.Error` with a `Usage` field keeps it, and the caller pulls it out with `errors.As`. The `Unwrap()` method keeps `errors.Is` working on whatever caused it.

**Files:**
- Create: `internal/briefer/briefer.go`
- Test: `internal/briefer/briefer_test.go`

**Interfaces:**
- Consumes: `prompt.System`, `prompt.Schema()`, `prompt.BuildUser`; `models.Briefing`, `models.Usage`, `models.PRContext`, `models.JiraContext`.
- Produces:
  - `type MessageCreator interface { CreateMessage(ctx context.Context, system, user string, schema map[string]any) (string, models.Usage, error) }`. The usage is meaningful even when the error is non-nil, if a response came back.
  - `const MaxLookItems = 6`.
  - `type Error struct { Reason string; Usage []models.Usage; Err error }` with `Error() string` (returns `Reason`, plus `": " + Err.Error()` when `Err` is set) and `Unwrap() error`.
  - `type Briefer struct { Client MessageCreator }` and `(*Briefer).Brief(ctx, pr models.PRContext, jira *models.JiraContext, jiraKey string) (models.Briefing, []models.Usage, error)`. Every error it returns is a `*Error`.

`Brief` steps: call `Client.CreateMessage(ctx, prompt.System, prompt.BuildUser(pr, jira, jiraKey), prompt.Schema())`; record usage (when `Model != ""`) in a one-element slice; on call error return `&Error{Reason: "model call failed", Usage, Err}`; unmarshal into `models.Briefing` (failure → `Reason: "unparseable briefing"`); empty trimmed `PRSummary` → `Reason: "empty briefing"`. Then clean up: drop `WhereToLook` items whose `File` isn't in `pr.ChangedFilePaths()`; negative `Line` → 0; keep the first `MaxLookItems`; drop `TicketAlignment` items whose status isn't `Valid()`; when `jira == nil`, set `TicketSummary = ""` and `TicketAlignment = nil`.

- [ ] **Step 1: Write the failing tests**

```go
type fakeClient struct {
	text   string
	usage  models.Usage
	err    error
	system string
	user   string
}

func (f *fakeClient) CreateMessage(ctx context.Context, system, user string, schema map[string]any) (string, models.Usage, error) {
	f.system, f.user = system, user
	return f.text, f.usage, f.err
}

func TestBriefReturnsParsedBriefingAndUsage(t *testing.T)
// valid JSON for a PR with file a.go; usage {claude-opus-5-5, 1000, 200}
// → PRSummary set; usage == []Usage{that}; f.system == prompt.System; f.user contains "=== FILE: a.go"

func TestBriefDropsLookItemsForUnknownFiles(t *testing.T)
// where_to_look files: "a.go", "nope.go", "a.go" (line -3) → 2 items, both a.go, second Line == 0

func TestBriefCapsLookItemsAtSix(t *testing.T)
// 9 valid items → 6, in original order

func TestBriefDropsInvalidAlignment(t *testing.T)
// statuses "met", "done", "gap" with jira non-nil → 2 items

func TestBriefClearsTicketFieldsWithoutJira(t *testing.T)
// jira nil, model returns ticket_summary "x" and one alignment → TicketSummary "" and TicketAlignment nil

func TestBriefWrapsCallErrorWithUsage(t *testing.T)
// f.err = errors.New("refused: cyber"), usage {claude-opus-5-5, 500, 0}
// → errors.As(err, &be) true; be.Reason == "model call failed"; be.Usage[0].InputTokens == 500;
//   errors.Is(err, f.err) true

func TestBriefRejectsUnparseableAndEmpty(t *testing.T)
// text "not json" → Reason "unparseable briefing"; text `{"pr_summary":"  "}` → Reason "empty briefing"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/briefer/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/briefer/briefer.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/briefer/ -v`
Expected: PASS, seven tests.

- [ ] **Step 5: Commit**

```bash
git add internal/briefer/
git commit -m "feat: add briefer with output validation"
```

---

### Task 11: Anthropic SDK adapter

> **Go concept: implicit interfaces.** `anthropicclient.Client` never mentions `briefer.MessageCreator`. It satisfies it by having the right method, and the compiler checks that where `main.go` assigns one to the other. Add `var _ briefer.MessageCreator = (*Client)(nil)` to get that check inside this package too.

**Files:**
- Create: `internal/anthropicclient/client.go`
- Test: `internal/anthropicclient/client_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `briefer.MessageCreator`, `models.Usage`.
- Produces: `anthropicclient.New(apiKey, model string, timeout time.Duration, opts ...option.RequestOption) *Client` (extra options exist so tests can pass `option.WithBaseURL`) and `(*Client).CreateMessage(ctx, system, user string, schema map[string]any) (string, models.Usage, error)`.

Request (non-streaming `client.Beta.Messages.New` on `github.com/anthropics/anthropic-sdk-go/v2`):
- `Model`: the configured model; `MaxTokens: 16000`; `System`: one text block; one user text message.
- `Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}`; `Fallbacks: anthropic.BetaFallbacksParamOfDefault()`.
- `OutputConfig`: `Effort` high and a `json_schema` format carrying `schema`. If the exact Go field or type names differ from these, write the call, run `go build ./...`, and follow the compiler (`go doc github.com/anthropics/anthropic-sdk-go/v2 BetaOutputConfigParam` lists the fields). Do not drop to raw JSON options unless no typed field exists.
- Do not set `Thinking` (always on for Opus 5.5) or `Temperature` (rejected).
- Client options: `option.WithAPIKey(apiKey)`, `option.WithRequestTimeout(timeout)`.

Response handling, in order: usage = `{resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens}` (use `resp.Model`, since a fallback can change it); `stop_reason == refusal` → error `refused: {stop_details.category}`; `stop_reason == max_tokens` → error `truncated at max_tokens=16000`; no text block → error `no text content`; else return the first text block's text. Always return the usage alongside an error when a response arrived.

- [ ] **Step 1: Add the SDK**

Run: `go get github.com/anthropics/anthropic-sdk-go/v2`
Expected: `go.mod` gains the requirement.

- [ ] **Step 2: Write the failing tests**

Use `httptest.NewServer` returning canned Messages API JSON, and pass `option.WithBaseURL(srv.URL)` and `option.WithMaxRetries(0)`.

```go
func TestCreateMessageSendsBriefingRequest(t *testing.T)
// handler decodes the request body into map[string]any and records the headers;
// asserts body["model"] == "claude-opus-5-5", body["max_tokens"] == 16000.0,
// body["fallbacks"] == "default", body["output_config"]["effort"] == "high",
// body["output_config"]["format"]["type"] == "json_schema", body has no "thinking" and no "temperature",
// header "anthropic-beta" contains "server-side-fallback-2026-07-01";
// responds {"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5",
//   "content":[{"type":"text","text":"{\"pr_summary\":\"x\"}"}],"stop_reason":"end_turn",
//   "usage":{"input_tokens":1200,"output_tokens":300}}
// → text == `{"pr_summary":"x"}`, usage == {claude-opus-5-5, 1200, 300}, err nil

func TestCreateMessageReportsFallbackModelInUsage(t *testing.T)
// response "model":"claude-opus-4-8" → usage.Model == "claude-opus-4-8"

func TestCreateMessageRefusal(t *testing.T)
// stop_reason "refusal", stop_details {"type":"refusal","category":"cyber"}, content []
// → err contains "refused: cyber"; usage.InputTokens set

func TestCreateMessageMaxTokens(t *testing.T)
// stop_reason "max_tokens" → err contains "max_tokens"
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/anthropicclient/`
Expected: FAIL to compile (`undefined: New`).

- [ ] **Step 4: Implement `internal/anthropicclient/client.go` per Interfaces**

- [ ] **Step 5: Run to verify pass**

Run: `go mod tidy && go test ./internal/anthropicclient/ -v && go vet ./...`
Expected: PASS, four tests.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/anthropicclient/
git commit -m "feat: add Anthropic adapter with structured output and fallback"
```

---

### Task 12: Slack rendering and posting

> **Go concept: `json.Marshal` on anonymous structs.** Block Kit payloads are nested JSON you send once and never read back. Small local types (`type block struct { Type string \`json:"type"\`; Text *text \`json:"text,omitempty"\` }`) keep the payload typed without a Slack SDK.

**Files:**
- Create: `internal/notify/render.go`, `internal/notify/slack.go`
- Test: `internal/notify/render_test.go`, `internal/notify/slack_test.go`

**Interfaces:**
- Consumes: `models.PullRequestSummary`, `models.Briefing`, `models.AlignmentStatus` constants.
- Produces:
  - `BriefingMessage{PR models.PullRequestSummary; JiraKey string; JiraFetched bool; FileCount, Additions, Deletions int; Briefing models.Briefing; DiffOnlyFiles []string}`.
  - `TooLargeMessage{PR models.PullRequestSummary; JiraKey, JiraTitle string; FileCount, Additions, Deletions int; EstimatedTokens int}`.
  - `RenderBriefingText(m BriefingMessage) string`, `RenderTooLargeText(m TooLargeMessage) string`: Slack mrkdwn, also used for stdout and dry-run logs.
  - `const maxSectionChars = 3000` and unexported `briefingBlocks(m BriefingMessage) []block`.
  - `NewSlack(webhookURL string, timeout time.Duration) *Slack`; methods `PostBriefing(ctx, m BriefingMessage) error`, `PostTooLarge(ctx, m TooLargeMessage) error`, `PostText(ctx, text string) error`. Each POSTs JSON `{"text": fallback[, "blocks": [...]]}`; a non-2xx status or transport error returns an error that includes the status and body. No logging-and-swallowing: the caller decides.

Briefing text layout (sections separated by a blank line; `esc` = replace `&`, `<`, `>` with `&amp;`, `&lt;`, `&gt;`, applied to every PR- or model-derived string):

```
:clipboard: *<{url}|{repo}#{n} — {esc title}>*
{jiraKey or "no ticket"} · {FileCount} files · +{a} −{d}

*What the ticket asks for*
{esc TicketSummary}                       | "No linked Jira ticket found." | "{key} couldn't be fetched."

*What the PR does*
{esc PRSummary}

*Ticket vs PR*                            (omitted when !JiraFetched)
:white_check_mark: {esc note}             (met)
:warning: {esc note}                      (gap, extra, unclear)

*Where to look*
• `{file}:{line}` — {esc why}             (`{file}` alone when line == 0)

_Diff only (size limits): a.go, b.go_     (only when DiffOnlyFiles non-empty)
```

Too-large text: header line as above, then `{jiraKey}: {esc JiraTitle}` when known, then `Too large to brief ({FileCount} files, +{a} −{d}, ~{EstimatedTokens} tokens of diff). Review without a head start.`

`briefingBlocks` emits one `section` block (`mrkdwn`) per text section above, each truncated to `maxSectionChars` runes with a trailing `…` when cut.

- [ ] **Step 1: Write the failing tests**

```go
func sampleMessage() BriefingMessage
// PR {URL "https://github.com/o/r/pull/7", RepoFullName "o/r", Number 7, Title "Fix paging"},
// JiraKey "ABC-1", JiraFetched true, FileCount 2, +10 −3,
// Briefing{TicketSummary "Fix last page.", PRSummary "Uses ceil.",
//   TicketAlignment [{met "Fixes bug"}, {gap "has_next untouched"}],
//   WhereToLook [{"pager.go", 41, "total=0"}, {"pager_test.go", 0, "no empty case"}]}

func TestRenderBriefingTextLayout(t *testing.T)
// contains "*<https://github.com/o/r/pull/7|o/r#7 — Fix paging>*", "ABC-1 · 2 files · +10 −3",
// "*What the ticket asks for*\nFix last page.", ":white_check_mark: Fixes bug", ":warning: has_next untouched",
// "• `pager.go:41` — total=0", "• `pager_test.go` — no empty case"; no "Diff only"

func TestRenderBriefingTextNoTicket(t *testing.T)
// JiraKey "", JiraFetched false → contains "No linked Jira ticket found." and "no ticket ·"; no "*Ticket vs PR*"

func TestRenderBriefingTextTicketFetchFailed(t *testing.T)
// JiraKey "ABC-1", JiraFetched false → contains "ABC-1 couldn't be fetched."; no "*Ticket vs PR*"

func TestRenderBriefingTextDiffOnlyFooter(t *testing.T)
// DiffOnlyFiles ["a.go","b.go"] → contains "_Diff only (size limits): a.go, b.go_"

func TestRenderEscapesSlackControlCharacters(t *testing.T)
// Title "Use List<T> & friends", PRSummary "if a && b < c", Why "returns <nil>"
// → contains "List&lt;T&gt; &amp; friends", "a &amp;&amp; b &lt; c", "&lt;nil&gt;"; the link "<https://" is intact

func TestBriefingBlocksTruncateLongSections(t *testing.T)
// PRSummary strings.Repeat("x", 5000) → every block's text has utf8.RuneCountInString <= 3000,
// the PR-summary block ends with "…"

func TestRenderTooLargeText(t *testing.T)
// JiraKey "ABC-1", JiraTitle "Big migration", 84 files, +6200 −3100, EstimatedTokens 210000
// → contains "ABC-1: Big migration", "Too large to brief (84 files, +6200 −3100, ~210000 tokens of diff)"

func TestPostBriefingSendsTextAndBlocks(t *testing.T)
// httptest server decodes body: has "text" == RenderBriefingText(m) and a non-empty "blocks" array;
// Content-Type "application/json"; 200 → nil error

func TestPostReturnsErrorOnNon2xx(t *testing.T)
// server replies 400 "invalid_blocks" → PostBriefing error contains "400" and "invalid_blocks"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/notify/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `render.go` and `slack.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/notify/ -v`
Expected: PASS, nine tests.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/
git commit -m "feat: render and post Slack briefings"
```

---

### Task 13: Weekly `digest`

> **Go concept: wall-clock arithmetic with `time.Date`.** Adding `7 * 24 * time.Hour` across a DST change lands an hour off. `time.Date(y, m, d, 9, 0, 0, 0, loc)` asks for "09:00 on that calendar day in that zone," and Go works out the offset. The Python version needed a tz workaround here; this one doesn't.

**Files:**
- Create: `internal/digest/digest.go`
- Test: `internal/digest/digest_test.go`

**Interfaces:**
- Consumes: `events.Event` and its type constants; `models.Usage`.
- Produces:
  - `Price{InputPerM, OutputPerM float64}` and `var Prices = map[string]Price{"claude-opus-5-5": {4.00, 20.00}}`.
  - `Boundary(now time.Time) time.Time`: the latest Monday 09:00 in `now.Location()` that is `<= now`.
  - `Due(evts []events.Event, now time.Time) bool`: true unless some `digest_sent` event has `PeriodEnd` not before `Boundary(now)`.
  - `Summary{Since, Until time.Time; Briefed, SkippedLarge, Failed, BudgetExhausted, UnpricedCalls int; TotalCost, MedianCost, MaxCost float64; MaxCostPR string}`.
  - `Compute(evts []events.Event, since, until time.Time) Summary`: considers events with `since <= TS < until`. `Briefed` counts `briefing_created`; `SkippedLarge` counts `briefing_skipped_large`; `Failed` counts `briefing_failed`; `BudgetExhausted` counts created events with that flag. Cost of one usage = `in/1e6*InputPerM + out/1e6*OutputPerM` from `Prices[usage.Model]`; an unknown model adds to `UnpricedCalls` and costs 0. `TotalCost` sums created and failed events. `MedianCost`/`MaxCost`/`MaxCostPR` are over created events only (median of an even count = mean of the middle two).
  - `Render(s Summary, unreadable int) string`: lines `*PR briefings: {Since:Jan 2} – {Until:Jan 2}*`, `Briefed: N · Too large: N · Failed: N`, `Cost: $T total · $M median · $X max (<{MaxCostPR}|max PR>)` (max part omitted when `MaxCostPR` is empty), `Full-file budget ran out on N PRs`, plus `N calls on unpriced models` and `N unreadable log lines skipped` only when non-zero. Money is `%.2f`.

- [ ] **Step 1: Write the failing tests**

```go
var chicago = mustLoad("America/Chicago")

func TestBoundary(t *testing.T)
// table in chicago:
//   Wed 2026-11-18 10:00 → Mon 2026-11-16 09:00
//   Mon 2026-11-16 09:00 → Mon 2026-11-16 09:00
//   Mon 2026-11-16 08:59 → Mon 2026-11-09 09:00
//   Sun 2026-11-22 23:00 → Mon 2026-11-16 09:00

func TestBoundaryAcrossDST(t *testing.T)
// DST ends Sun 2026-11-01 in Chicago. now = Mon 2026-11-02 12:00 CST →
// Boundary == time.Date(2026, 11, 2, 9, 0, 0, 0, chicago); its Hour() == 9;
// Boundary(Mon 2026-11-02 08:30) == time.Date(2026, 10, 26, 9, 0, 0, 0, chicago), Hour() == 9

func TestDue(t *testing.T)
// now Wed 2026-11-18 10:00 chicago; no events → true;
// digest_sent PeriodEnd == Mon 11-16 09:00 → false;
// digest_sent PeriodEnd == Mon 11-09 09:00 → true

func TestComputeCostsAndCounts(t *testing.T)
// within window: created A usage {opus-5-5, 100_000, 2_000} (cost 0.44),
//                created B {opus-5-5, 10_000, 1_000} (0.06) BudgetExhausted true,
//                failed C {opus-5-5, 50_000, 0} (0.20),
//                skipped_large D, created E usage {"mystery-model", 1, 1};
// outside window: created F
// → Briefed 3, SkippedLarge 1, Failed 1, BudgetExhausted 1, UnpricedCalls 1;
//   TotalCost ≈ 0.70, MaxCost ≈ 0.44, MaxCostPR == A's PRURL, MedianCost ≈ 0.06 (median of 0.44, 0.06, 0)
// compare floats with math.Abs(got-want) < 1e-9

func TestRender(t *testing.T)
// Summary with Briefed 3, TotalCost 0.7, MedianCost 0.06, MaxCost 0.44, MaxCostPR "https://x", UnpricedCalls 0
// → contains "Briefed: 3", "$0.70 total", "$0.06 median", "$0.44 max (<https://x|max PR>)";
//   no "unpriced"; Render(s, 2) contains "2 unreadable log lines skipped"
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/digest/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/digest/digest.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/digest/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/digest/
git commit -m "feat: add weekly cost and volume digest"
```

---

### Task 14: `poller`: pipeline and run loop

> **Go concept: interfaces belong to the consumer.** `poller` declares the four small interfaces it needs (`GitHub`, `Jira`, `Model`, `Notifier`) instead of importing concrete clients. The real clients satisfy them without knowing it, and tests swap in fakes of a few lines each. This is why no other package's tests needed a network.

**Files:**
- Create: `internal/poller/pipeline.go`, `internal/poller/runner.go`
- Test: `internal/poller/pipeline_test.go`, `internal/poller/runner_test.go`

**Interfaces:**
- Consumes: `budget` (Task 7), `jira.ExtractKey` (Task 8), `briefer.Error` (Task 10), `notify` messages and renderers (Task 12), `state` (Task 3), `events` (Task 4), `digest` (Task 13).
- Produces:
  - `type GitHub interface { ListTeamReviewRequests(ctx) ([]models.PullRequestSummary, error); GetPRContext(ctx, models.PullRequestSummary) (models.PRContext, error); GetFileContent(ctx, repo, path, ref string) (string, error) }`
  - `type Jira interface { FetchTicket(ctx, key string) (*models.JiraContext, error) }`
  - `type Model interface { Brief(ctx, models.PRContext, *models.JiraContext, string) (models.Briefing, []models.Usage, error) }`
  - `type Notifier interface { PostBriefing(ctx, notify.BriefingMessage) error; PostTooLarge(ctx, notify.TooLargeMessage) error; PostText(ctx, string) error }`
  - `type Kind int` with `KindBriefing`, `KindTooLarge`.
  - `type Result struct { Kind Kind; JiraKey string; Briefing notify.BriefingMessage; TooLarge notify.TooLargeMessage; Usage []models.Usage; FullFiles, DiffOnlyFiles []string; BudgetExhausted bool; EstimatedTokens int }`
  - `type Pipeline struct { GitHub GitHub; Jira Jira; Model Model; ProjectKeys []string; Budget int }` and `(*Pipeline).Prepare(ctx, summary models.PullRequestSummary) (Result, error)`.
  - `type Runner struct { Pipeline *Pipeline; GitHub GitHub; Notifier Notifier; State *state.State; Events *events.Log; DryRun bool; Model string; PerPRTimeout time.Duration; Now func() time.Time }` and `(*Runner).RunOnce(ctx) error`.

`Prepare`, in order:
1. `GetPRContext`; error → return it.
2. `key := jira.ExtractKey(branch, body, ProjectKeys)`; if non-empty, `FetchTicket`; error → `slog.Warn` and continue with `nil` ticket.
3. `diff := budget.DiffTokens(pr)`; `FileCount = len(Files) + len(OtherChanges)`; `Additions, Deletions = pr.Totals()`.
4. `diff > Budget` → return `KindTooLarge` with `EstimatedTokens = diff` and the ticket title if fetched. No content fetches, no model call.
5. For each `budget.Eligible(pr)` path, `GetFileContent(ctx, repo, path, HeadSHA)`; an error → `slog.Warn` and that path goes to `DiffOnlyFiles`.
6. `sel := budget.Select(diff, contents, Budget)`; set `FullContent` on the selected `Files`; `DiffOnlyFiles = sorted(sel.DiffOnly ∪ fetch failures)`.
7. `Model.Brief(ctx, pr, ticket, key)`; error → return `Result{Usage: …}` and the error unchanged (it is a `*briefer.Error`).
8. Return `KindBriefing` with `notify.BriefingMessage{…, JiraFetched: ticket != nil}`.

`RunOnce`, in order:
1. `ListTeamReviewRequests`; error → return it.
2. For each `State.Pending(prs)`: `context.WithTimeout(ctx, PerPRTimeout)`, then `Prepare`.
   - Error that is a `*briefer.Error` → failure path (below) with its `Reason` and `Usage`.
   - Any other error → `slog.Error` and move on (no failure count: GitHub hiccups retry next poll).
   - `KindBriefing`: dry run → `slog.Info` with `RenderBriefingText`, `State.MarkDryRun`, event `briefing_created` with `DryRun: true`. Otherwise `PostBriefing`; error → failure path with reason `"slack: " + err.Error()` and the result's usage; success → `State.MarkBriefed`, event `briefing_created` (PRURL, Repo, Number, Title, Model, JiraKey, Usage, FullFiles, DiffOnlyFiles, BudgetExhausted).
   - `KindTooLarge`: same shape with `RenderTooLargeText`/`PostTooLarge` and event `briefing_skipped_large` (PRURL, Repo, Number, Title, EstimatedTokens, Files).
   - Failure path: event `briefing_failed` (PRURL, Model, Reason, Usage); `n := State.IncrementFailed`; `n >= 3` → `State.MarkSkippedManual` and `slog.Error("giving up …")`.
   - Any `State` write error → return it immediately: a bot that can't record what it posted will post it again every 15 minutes.
3. Digest: `Events.Read()`; read error → log and return nil. If `digest.Due(evts, Now())`: `b := digest.Boundary(Now())`, `text := digest.Render(digest.Compute(evts, b.AddDate(0, 0, -7), b), unreadable)`; dry run → log it; else `PostText`, and on error log and return nil. Then append `digest_sent` with `PeriodStart`/`PeriodEnd`.

- [ ] **Step 1: Write the fakes and failing tests**

Fakes record calls: `fakeGitHub{prs; ctxs map[url]PRContext; contents map[path]string; contentErr map[path]error; ctxErr map[url]error; contentCalls int}`, `fakeJira{tickets map[key]*JiraContext; err error}`, `fakeModel{briefing; usage; err; calls int; lastPR PRContext}`, `fakeNotifier{briefings []BriefingMessage; tooLarge []TooLargeMessage; texts []string; err error}`. A helper `newRunner(t, gh, model, notifier)` builds state and event log in `t.TempDir()` with fixed clocks at Wed 2026-11-18 10:00 local, `Budget: budget.TokenBudget`, `PerPRTimeout: time.Minute`.

Pipeline tests:

```go
func TestPrepareMarksFullAndDiffOnlyFiles(t *testing.T)
// files small.go (content 100 bytes), huge.go (content strings.Repeat("x\n", 2500)), go.sum (skipped)
// → model saw FullContent on small.go only; Result.FullFiles ["small.go"]; DiffOnlyFiles ["huge.go"];
//   GetFileContent never called for go.sum

func TestPrepareContentFetchFailureIsDiffOnly(t *testing.T)
// contentErr["a.go"] = errors.New("boom") → DiffOnlyFiles contains "a.go"; Brief still called

func TestPrepareTooLargeSkipsModelAndFetches(t *testing.T)
// one file with Patch strings.Repeat("x", 4*150_001) and branch "ABC-1-big", ticket {Key ABC-1, Title "Big"}
// → Kind == KindTooLarge, TooLarge.JiraTitle == "Big", EstimatedTokens > 150_000,
//   model.calls == 0, gh.contentCalls == 0

func TestPrepareJiraFailureStillBriefs(t *testing.T)
// branch "ABC-1-x", fakeJira.err set → Kind KindBriefing, Briefing.JiraKey "ABC-1", JiraFetched false
```

Runner tests:

```go
func TestRunOnceBriefsPostsAndRecords(t *testing.T)
// one pending PR → notifier.briefings len 1; State.IsDone true; reading the event log gives one
// briefing_created with Usage[0].InputTokens from fakeModel and DryRun false

func TestRunOnceSkipsDonePRs(t *testing.T)
// State.MarkBriefed(pr.URL) first → model.calls == 0, no posts

func TestRunOnceTooLargePostsNotice(t *testing.T)
// → notifier.tooLarge len 1, IsDone true, one briefing_skipped_large event, model.calls 0

func TestRunOnceGivesUpAfterThreeModelFailures(t *testing.T)
// model.err = &briefer.Error{Reason: "model call failed"}; RunOnce three times
// → no posts; three briefing_failed events; after run 3 IsDone true (skipped_manual); fourth run model.calls stays 3

func TestRunOnceSlackFailureDoesNotMarkBriefed(t *testing.T)
// notifier.err set → IsDone false after one run; one briefing_failed event whose Reason starts with "slack: ";
// the event carries the model usage

func TestRunOnceDryRunDoesNotPost(t *testing.T)
// DryRun true → no posts; IsDone true; briefing_created event with DryRun true

func TestRunOnceGitHubErrorOnOnePRContinues(t *testing.T)
// two PRs, ctxErr on the first → second briefed; first not done and has no briefing_failed event

func TestRunOnceSendsDigestOncePerWeek(t *testing.T)
// no PRs; first RunOnce → notifier.texts len 1 and one digest_sent event with PeriodEnd == Mon 2026-11-16 09:00 local;
// second RunOnce → texts still len 1
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/poller/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `pipeline.go` and `runner.go` per Interfaces**

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/poller/ -v && go test ./... && go vet ./...`
Expected: PASS, twelve poller tests; every package `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/poller/
git commit -m "feat: add briefing pipeline and run loop"
```

---

### Task 15: `main.go`

> **Go concept: `signal.NotifyContext`.** It gives you a context that's cancelled on Ctrl-C or launchd's SIGTERM. Because every call takes that context, a stop mid-run cancels the in-flight HTTP request instead of waiting out a 5-minute model call.

**Files:**
- Create: `cmd/pr-review-bot/main.go`

**Interfaces:**
- Consumes: every package above.
- Produces: the `pr-review-bot` binary. Flags: `-pr <url>` (one-off briefing), `-post` (with `-pr`, also send to Slack), `-version` (prints `var version = "dev"`, overridable with `-ldflags "-X main.version=…"`).

Behavior:
- `config.Load(true)`; error → print to stderr, exit 1.
- `slog` text handler on stderr at `LOG_LEVEL` (`DEBUG`/`INFO`/`WARN`/`ERROR`, default INFO).
- Wire `ghclient.New(cfg.GitHubPAT, cfg.GitHubTeamSlug, cfg.PRMaxAgeDays, cfg.HTTPTimeout)`, `jira.New(...)`, `&briefer.Briefer{Client: anthropicclient.New(cfg.AnthropicAPIKey, cfg.Model, cfg.AnthropicTimeout)}`, `notify.NewSlack(cfg.SlackWebhookURL, cfg.HTTPTimeout)`, `poller.Pipeline{…, ProjectKeys: cfg.JiraProjectKeys, Budget: budget.TokenBudget}`.
- **`-pr` mode:** `GetPullRequest(url)` → `Prepare` → print `RenderBriefingText` or `RenderTooLargeText` to stdout, then one stderr line per usage `model in=N out=N cost≈$X` using `digest.Prices`. With `-post` and not `DRY_RUN`, also post. No state, no events. A `*briefer.Error` prints its reason and exits 1.
- **Poll mode:** `state.Load(cfg.StatePath)` (error → log, exit 1), `events.Open(cfg.EventsPath)`, `poller.Runner{…, DryRun: cfg.DryRun, Model: cfg.Model, PerPRTimeout: 6 * time.Minute, Now: time.Now}.RunOnce(ctx)`; error → log, exit 1.

- [ ] **Step 1: Write `cmd/pr-review-bot/main.go` per Interfaces**

- [ ] **Step 2: Build and smoke-test the flags**

Run: `go build -o pr-review-bot ./cmd/pr-review-bot && ./pr-review-bot -version && go vet ./...`
Expected: prints `dev`; vet silent.

Run: `env -i PATH="$PATH" ./pr-review-bot; echo "exit=$?"`
Expected: a "missing required env vars: GITHUB_PAT, …" message and `exit=1`.

- [ ] **Step 3: Commit**

```bash
git add cmd/pr-review-bot/
git commit -m "feat: add CLI entrypoint with one-off -pr mode"
```

---

### Task 16: Tune, install, and cut over

**Files:**
- Create: `.env.example`, `com.elinzer.go-pr-review-bot.plist`, `README.md`
- Modify: `.gitignore`

- [ ] **Step 1: Repo files**

`.gitignore` adds `briefings.jsonl` and `state.json.tmp`. `.env.example` lists every var from Task 2 with the defaults commented. The plist is the July plan's Task 15 plist with `Label` `com.elinzer.go-pr-review-bot`, `StartInterval` `900`, and logs under `~/Library/Logs/go-pr-review-bot/`.

- [ ] **Step 2: Tune the prompt on real PRs (spends API money; roughly $0.10–0.15 per run)**

```bash
cp ../pr-review-bot/.env .env && chmod 600 .env
go build -o pr-review-bot ./cmd/pr-review-bot
./pr-review-bot -pr <url of a recent PR you reviewed>
```
Expected: a briefing on stdout and a cost line on stderr. Repeat on 3–5 PRs you already know (one with a ticket, one without, one large). Edit `internal/prompt/briefing_system.md`, rebuild, re-run until the briefings would have saved you time. Commit prompt changes as `feat: tune briefing prompt`.

- [ ] **Step 3: Dry-run poll against its own state file**

```bash
cp ../pr-review-bot/state.json ./state.json
DRY_RUN=true STATE_PATH=./state.json EVENTS_PATH=/tmp/briefings-dry.jsonl LOG_LEVEL=DEBUG ./pr-review-bot
```
Expected: the same open-PR count the Python bot logs; already-reviewed PRs are not briefed; any new PR's briefing appears in the log. This copy is never shared with Python.

- [ ] **Step 4: Cut over**

```bash
launchctl unload ~/Library/LaunchAgents/com.elinzer.pr-review-bot.plist
cp ../pr-review-bot/state.json ./state.json
# set DRY_RUN=false in .env
mkdir -p ~/Library/Logs/go-pr-review-bot
sed -e "s|__PROJECT_PATH__|$(pwd)|g" -e "s|__HOME__|$HOME|g" com.elinzer.go-pr-review-bot.plist \
  > ~/Library/LaunchAgents/com.elinzer.go-pr-review-bot.plist
launchctl load ~/Library/LaunchAgents/com.elinzer.go-pr-review-bot.plist
tail -f ~/Library/Logs/go-pr-review-bot/stderr.log
```
Expected: one run at load. The next review request produces a Slack briefing, and the following poll logs it as done.

- [ ] **Step 5: README**

Cover what the bot does, setup, build, `-pr` tuning, the launchd install and pause steps (unload **and** remove the plist so it stays off across logins, as in the Python README's latest fix), log locations, and "Differences from the Python bot": briefings instead of pending reviews, read-only GitHub, cost digest only.

- [ ] **Step 6: Final verification and commit**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: every package `ok`, vet silent, `gofmt -l` prints nothing.

```bash
git add .gitignore .env.example com.elinzer.go-pr-review-bot.plist README.md
git commit -m "chore: add launchd agent, env example, and README"
```
