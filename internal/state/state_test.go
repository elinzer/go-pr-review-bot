package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elinzer/go-pr-review-bot/internal/models"
)

const testURL = "https://github.com/o/r/pull/1"

func newTestState(t *testing.T) (*State, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Now = func() time.Time {
		return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	}
	return s, path
}

func readRaw(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Reviews map[string]map[string]any `json:"reviews"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return doc.Reviews
}

func reload(t *testing.T, path string) *State {
	t.Helper()
	s, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return s
}

func TestLoadMissingFileStartsEmpty(t *testing.T) {
	s, _ := newTestState(t)
	if s.IsDone(testURL) {
		t.Errorf("IsDone(%q) = true on empty state, want false", testURL)
	}
}

func TestLoadCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load() on corrupt file returned nil error, want error")
	}
}

func TestMarkBriefedPersistsAndWritesBriefedAt(t *testing.T) {
	s, path := newTestState(t)
	if err := s.MarkBriefed(testURL); err != nil {
		t.Fatalf("MarkBriefed: %v", err)
	}
	if !s.IsDone(testURL) {
		t.Error("IsDone = false after MarkBriefed, want true")
	}
	if !reload(t, path).IsDone(testURL) {
		t.Error("IsDone = false after reload, want true")
	}

	e := readRaw(t, path)[testURL]
	if got := e["briefed_at"]; got != "2026-11-18T12:00:00Z" {
		t.Errorf("briefed_at = %v, want 2026-11-18T12:00:00Z", got)
	}
	for _, key := range []string{"review_id", "skipped_manual", "parse_failed_count"} {
		if _, ok := e[key]; ok {
			t.Errorf("key %q present in entry %v, want absent", key, e)
		}
	}
}

func TestMarkDryRunCountsAsDone(t *testing.T) {
	s, path := newTestState(t)
	if err := s.MarkDryRun(testURL); err != nil {
		t.Fatalf("MarkDryRun: %v", err)
	}
	if !s.IsDone(testURL) {
		t.Error("IsDone = false after MarkDryRun, want true")
	}
	if got := readRaw(t, path)[testURL]["dry_run_logged_at"]; got != "2026-11-18T12:00:00Z" {
		t.Errorf("dry_run_logged_at = %v, want 2026-11-18T12:00:00Z", got)
	}
}

func TestIncrementFailedAccumulatesWithoutMarkingDone(t *testing.T) {
	s, path := newTestState(t)
	for want := 1; want <= 3; want++ {
		got, err := s.IncrementFailed(testURL)
		if err != nil {
			t.Fatalf("IncrementFailed: %v", err)
		}
		if got != want {
			t.Errorf("IncrementFailed call %d returned %d, want %d", want, got, want)
		}
		if s.IsDone(testURL) {
			t.Errorf("IsDone = true after %d failures, want false", want)
		}
	}
	if got := readRaw(t, path)[testURL]["parse_failed_count"]; got != float64(3) {
		t.Errorf("parse_failed_count on disk = %v, want 3", got)
	}
}

func TestMarkSkippedManualIsDone(t *testing.T) {
	s, path := newTestState(t)
	if err := s.MarkSkippedManual(testURL); err != nil {
		t.Fatalf("MarkSkippedManual: %v", err)
	}
	if !s.IsDone(testURL) {
		t.Error("IsDone = false after MarkSkippedManual, want true")
	}
	if !reload(t, path).IsDone(testURL) {
		t.Error("IsDone = false after reload, want true")
	}
}

func TestPendingFiltersDone(t *testing.T) {
	s, _ := newTestState(t)
	prs := []models.PullRequestSummary{
		{URL: "https://github.com/o/r/pull/1"},
		{URL: "https://github.com/o/r/pull/2"},
		{URL: "https://github.com/o/r/pull/3"},
	}
	if err := s.MarkBriefed(prs[0].URL); err != nil {
		t.Fatalf("MarkBriefed: %v", err)
	}
	if err := s.MarkSkippedManual(prs[1].URL); err != nil {
		t.Fatalf("MarkSkippedManual: %v", err)
	}

	got := s.Pending(prs)
	if len(got) != 1 || got[0].URL != prs[2].URL {
		t.Errorf("Pending = %v, want only %s", got, prs[2].URL)
	}
}

func TestReadsAndPreservesPythonEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := `{"reviews":{
		"https://github.com/o/r/pull/42":{"review_id":999,"reviewed_at":"2026-06-19T17:37:00+00:00"},
		"https://github.com/o/r/pull/43":{"skipped_manual":true,"parse_failed_count":3},
		"https://github.com/o/r/pull/44":{"dry_run_logged_at":"2026-09-30T10:00:00+00:00"}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := reload(t, path)
	for _, n := range []string{"42", "43", "44"} {
		url := "https://github.com/o/r/pull/" + n
		if !s.IsDone(url) {
			t.Errorf("IsDone(%s) = false, want true", url)
		}
	}

	if err := s.MarkBriefed("https://github.com/o/r/pull/45"); err != nil {
		t.Fatalf("MarkBriefed: %v", err)
	}
	e := readRaw(t, path)["https://github.com/o/r/pull/42"]
	if got := e["review_id"]; got != float64(999) {
		t.Errorf("review_id = %v, want 999", got)
	}
	if got := e["reviewed_at"]; got != "2026-06-19T17:37:00+00:00" {
		t.Errorf("reviewed_at = %v, want 2026-06-19T17:37:00+00:00", got)
	}
}
