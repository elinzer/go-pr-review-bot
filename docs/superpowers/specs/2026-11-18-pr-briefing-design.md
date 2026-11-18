# PR Briefing Bot (Go) — Design

**Date:** 2026-11-18
**Supersedes (in part):** `docs/superpowers/plans/2026-07-29-go-port.md`

## Intent

The Python `pr-review-bot` drafts a pending GitHub review with `bug`/`question` comments. Its own outcome log shows this isn't working: of 28 resolved reviews, 17 were never published (14 ignored, 3 discarded); on the 11 that were published, 8 of 10 generated comments were deleted and 0 kept verbatim, 8 of 11 summaries were rewritten, and the human added 9 comments of their own.

The Go version changes the bot's job. It no longer reviews on the human's behalf. It **briefs the human reviewer** so they start a review with a head start: what the Jira ticket asks for, what the PR actually does, where the two diverge, and where in the code to look hard. The human writes every review comment.

The Go port is also a learning project; the implementation plan keeps the existing plan's "Go concept" teaching notes and test-first task structure.

## Goals

- For each PR that requests review from the configured GitHub team, post one Slack message (the briefing) containing:
  1. **What the ticket asks for** — a short summary of the Jira ticket (plus epic/linked-issue context where it matters).
  2. **What the PR does** — a short summary of the change, informed by the diff and full file contents.
  3. **Ticket vs PR** — where the PR satisfies the ticket, and where it appears to miss, exceed, or contradict it.
  4. **Where to look** — a handful of specific spots (`file:line` where possible) with *why* each deserves attention.
- Give the model the diff **plus the full contents of each changed file** at the PR head, within cost limits.
- Track cost and volume, and send a weekly Slack digest of it.
- Replace the Python bot as a single static Go binary run by launchd.

## Non-goals

- Writing anything to GitHub: no pending reviews, no comments, no labels. The GitHub token needs read access only.
- Agentic repo exploration (tool loops, following callers into unchanged files).
- Measuring whether briefings are helpful. The human will track that separately.
- The Python `stats` CLI, outcome tracker, and edit-similarity metrics.
- Re-briefing a PR when new commits are pushed. A PR is briefed once.
- Modifying the Python bot. It runs unchanged until the Go binary takes over.

## Anti-goals

- A briefing that restates the diff line by line. If it isn't faster to read than the diff, it failed.
- Confident claims about code the model didn't see. The prompt must make it say "verify X" rather than assert.
- Anything visible to the PR author or other reviewers.
- Surprise cost from a huge PR.

## Constraints

- Go 1.26; module `github.com/elinzer/go-pr-review-bot`.
- Dependencies: `github.com/anthropics/anthropic-sdk-go`, `github.com/joho/godotenv`, stdlib. No GitHub SDK, test framework, assertion library, or logging library.
- Every outbound call takes a `context.Context`; every HTTP client has a timeout.
- Must read the existing Python `state.json` so already-reviewed PRs are not briefed again at cutover.
- No code comments unless the behavior is dangerous and non-obvious.

## Approaches considered

- **A. Rewrite the Go plan around briefings (chosen).** Keep the still-valid port tasks (models, config, state, Jira, GitHub reads, Slack), replace the reviewer, pending-review, and tracker work with briefing work. One effort, nothing ported only to be deleted; prompt tuning happens in Go via `DRY_RUN`.
- **B. Prototype briefings in Python first, then port.** Fastest validation of the prompt, but does the change twice and touches a bot otherwise left alone.
- **C. Port as-is, then change in Go.** Rejected: roughly a third of the existing plan (evidence validation, critique pass, pending reviews, tracker) would be built then deleted.

## Design

### Flow (one run, launched by launchd every 15 minutes)

1. Load config and state. Open the event log.
2. Search GitHub for open, non-draft PRs requesting the team, created within `PR_MAX_AGE_DAYS` (same query as Python).
3. Filter out PRs already in state as reviewed, briefed, or skipped.
4. For each remaining PR:
   1. Fetch PR files (paginated). Classify each into: has-patch, renamed-without-change, no-patch (binary/too large) — same as Python `get_pr_context`.
   2. Fetch full contents at `head_sha` for eligible changed files, subject to the limits below.
   3. Extract the Jira key (branch first, then body filtered by `JIRA_PROJECT_KEYS`) and fetch the ticket (summary, description as flattened ADF, parent/epic, issue links) — same as Python.
   4. One Anthropic call with structured output returns the briefing JSON.
   5. Lightly validate: drop any "where to look" item whose file isn't in the PR; clamp item count.
   6. Post the briefing to Slack (or log it in `DRY_RUN`).
   7. On success, mark briefed in state and append a `briefing_created` event.
5. If a weekly digest is due, compute and post it.

### Full-file limits

- **Skip list** (diff only, never full file): lockfiles (`go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `poetry.lock`, `Pipfile.lock`, `Cargo.lock`, `Gemfile.lock`), `vendor/`, `node_modules/`, `*.min.js`, `*.min.css`, `*.map`, `*.snap`, `__snapshots__/`, files with a `generated`/`.pb.go`/`_gen.go` marker, deleted files, binary files.
- **Per-file cap:** files over 2,000 lines get diff only.
- **Per-PR budget:** ~150k estimated tokens of total input (estimate: bytes ÷ 4). The diff counts first; full files fill whatever remains, smallest-first; once the budget is exhausted, remaining files get diff only.
- **Oversized PRs are skipped.** If the diff alone exceeds the budget, make no model call. Post a short Slack message instead: repo#number, title, link, the Jira key and ticket title if found, file count and `+X −Y`, and "too large to brief". Mark the PR briefed in state and append a `briefing_skipped_large` event (pr_url, repo, number, estimated_tokens, files). In `-pr` mode, print the same message.
- The prompt labels each file as "full file + diff" or "diff only" so the model knows what it saw.

Expected cost at Opus 5.5 pricing ($4/M in, $20/M out): ~$0.10–0.15 for a typical PR, under ~$1 worst case (150k in + 16k out). Today's median is $0.07.

### Model call

- Model `claude-opus-5-5` (env `MODEL` overrides), `effort: high`, `max_tokens: 16000`, structured output via JSON schema, server-side fallback beta — matching the Python `Reviewer._call`.
- Treat `stop_reason` `refusal` and `max_tokens` as failures; record usage (model, input/output tokens) even on failure.
- One pass only. No critique pass.
- Briefing schema:

```json
{
  "ticket_summary": "string — empty if no ticket",
  "pr_summary": "string",
  "ticket_alignment": [
    {"status": "met | gap | extra | unclear", "note": "string"}
  ],
  "where_to_look": [
    {"file": "path", "line": 0, "why": "string"}
  ]
}
```

`line` is optional (0 = whole file). `where_to_look` is capped at 6 items.

- Prompt guidance (exact wording is the builder's): write for a senior engineer about to review; be brief; prioritize risk (behavior changes, edge cases, error paths, concurrency, data/migrations, security, callers likely affected); say "verify" rather than assert when the concern depends on code not shown; don't comment on style; don't restate the diff; use the Jira ticket as intent, not as proof of behavior.

### Slack message

Posted via the existing incoming webhook as Block Kit with a plain `text` fallback. Layout:

```
📋 spothero/widgets#482 — Fix off-by-one in pagination
   ABC-123 · 4 files · +38 −12 · <link>

What the ticket asks for
  ...

What the PR does
  ...

Ticket vs PR
  ✅ ...   (met)
  ⚠️ ...   (gap / extra / unclear)

Where to look
  • paginator.go:41 — ...
```

- No ticket found: the ticket section reads "No linked Jira ticket found" and "Ticket vs PR" is omitted. If a key was found but fetch failed, say so ("ABC-123 couldn't be fetched").
- Escape `&`, `<`, `>` in model-produced text.
- Files that were diff-only because of limits are noted in a small footer line so the human knows the briefing's blind spots.

### State

`state.json` keeps its shape. A new optional key `briefed_at` (RFC 3339) marks a briefed PR. A PR is considered done if it has `review_id`, `dry_run_logged_at`, `briefed_at`, or `skipped_manual: true`. Python-written keys are preserved on rewrite.

Failure handling: a model failure **or a Slack post failure** increments `parse_failed_count` (key name kept for compatibility); at 3, set `skipped_manual`. A PR is marked briefed only after Slack accepts the post, because Slack is now the only output.

`DRY_RUN`: log the full rendered briefing, set `dry_run_logged_at`, append event with `dry_run: true`, don't post.

### Running alongside the Python bot, and cutover

The Go binary never shares a state file with the running Python bot: both rewrite the whole file, and Python treats `dry_run_logged_at` as done, so sharing would silently suppress Python reviews. Until cutover, Go uses its own `STATE_PATH`. Cutover: unload the Python launchd job, copy the Python `state.json` over Go's state file (so already-reviewed PRs aren't briefed), then load the Go launchd job.

### One-off mode for prompt tuning

`pr-review-bot -pr <url>` briefs a single PR by URL: no team search, no state reads or writes, and no events. It prints the rendered briefing to stdout and posts to Slack only if `-post` is also given. This is the prompt-tuning loop, and it can run repeatedly against any PR, open or closed.

### Events and digest

- JSONL at `EVENTS_PATH` (default `./briefings.jsonl`, a new file, so the old review events don't mix in).
- Events: `briefing_created` (pr_url, repo, number, title, model, jira_key, usage[], full_files, diff_only_files, budget_exhausted, dry_run), `briefing_skipped_large`, `briefing_failed` (pr_url, model, reason, usage[]), `digest_sent` (period_start, period_end).
- Weekly digest Monday 09:00 local, same "due if no `digest_sent` covers the latest boundary" rule as Python. Contents: PRs briefed, PRs skipped as too large, failures, total cost, median and max cost per PR (with the max PR's link), count of PRs where the full-file budget ran out.
- Pricing table in code: `claude-opus-5-5` → $4 / $20 per million tokens; unpriced models counted separately.

### Config

Required: `GITHUB_PAT`, `ANTHROPIC_API_KEY`, `JIRA_EMAIL`, `JIRA_API_TOKEN`, `JIRA_BASE_URL`, `GITHUB_TEAM_SLUG`, and `SLACK_WEBHOOK_URL` unless `DRY_RUN` is true.
Optional: `PR_MAX_AGE_DAYS` (14), `MODEL` (`claude-opus-5-5`), `STATE_PATH` (`./state.json`), `EVENTS_PATH` (`./briefings.jsonl`), `DRY_RUN`, `LOG_LEVEL` (`INFO`), `JIRA_PROJECT_KEYS` (comma list, uppercased).
`POLL_INTERVAL_SECONDS` is dropped; launchd owns scheduling. Internal timeouts: 10s HTTP, 5m Anthropic.

### Impact on work already done

- `internal/models` (committed): drop `Evidence`, `Severity`, `Comment`, `Review`; add `FileChange.PreviousPath`, a full-content field, `PRContext.OtherChanges`, `JiraContext.Epic`/`LinkedIssues` (drop `AcceptanceCriteria`), and briefing types.
- `internal/config` (in progress, uncommitted): update to the config above.

## Calls made without the human (marked for review)

- Brief once per PR; no re-brief on new pushes.
- Limit numbers (2,000 lines, 150k tokens, 6 look-items) and the skip list.
- Slack failure counts toward the 3-strikes skip; `SLACK_WEBHOOK_URL` becomes required.
- New events file rather than appending to `reviews.jsonl`.
- `briefed_at` as the new state key.
- Separate Go state file until cutover; cutover copies Python state.
- `-pr <url>` one-off mode (stateless, stdout by default, `-post` to send) for prompt tuning.
- Oversized-PR notice includes the Jira title (no model call) and marks the PR done so it isn't retried every poll.

## Left to the builder

- Exact prompt wording and schema field descriptions.
- Which GitHub endpoint fetches file content (contents API with raw media type vs. the file's `raw_url`), and handling of its size limits.
- How to pass beta/structured-output parameters through the Go SDK (typed fields vs. raw JSON options).
- Package layout, beyond keeping one responsibility per package.
- Block Kit specifics.
