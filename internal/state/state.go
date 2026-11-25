package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/elinzer/go-pr-review-bot/internal/models"
)

type entry struct {
	ReviewID         *int64 `json:"review_id,omitempty"`
	ReviewedAt       string `json:"reviewed_at,omitempty"`
	BriefedAt        string `json:"briefed_at,omitempty"`
	DryRunLoggedAt   string `json:"dry_run_logged_at,omitempty"`
	ParseFailedCount int    `json:"parse_failed_count,omitempty"`
	SkippedManual    bool   `json:"skipped_manual,omitempty"`
}

func (e entry) done() bool {
	return e.ReviewID != nil || e.DryRunLoggedAt != "" || e.BriefedAt != "" || e.SkippedManual
}

type fileShape struct {
	Reviews map[string]entry `json:"reviews"`
}

type State struct {
	Now  func() time.Time
	path string
	data fileShape
}

func Load(path string) (*State, error) {
	s := &State{Now: time.Now, path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read state %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("parse state %s: %w", path, err)
		}
	}
	if s.data.Reviews == nil {
		s.data.Reviews = map[string]entry{}
	}
	return s, nil
}

func (s *State) IsDone(url string) bool {
	return s.data.Reviews[url].done()
}

func (s *State) Pending(prs []models.PullRequestSummary) []models.PullRequestSummary {
	var out []models.PullRequestSummary
	for _, pr := range prs {
		if !s.IsDone(pr.URL) {
			out = append(out, pr)
		}
	}
	return out
}

func (s *State) MarkBriefed(url string) error {
	return s.update(url, func(e *entry) { e.BriefedAt = s.timestamp() })
}

func (s *State) MarkDryRun(url string) error {
	return s.update(url, func(e *entry) { e.DryRunLoggedAt = s.timestamp() })
}

func (s *State) MarkSkippedManual(url string) error {
	return s.update(url, func(e *entry) { e.SkippedManual = true })
}

func (s *State) IncrementFailed(url string) (int, error) {
	var n int
	err := s.update(url, func(e *entry) {
		e.ParseFailedCount++
		n = e.ParseFailedCount
	})
	return n, err
}

func (s *State) timestamp() string {
	return s.Now().UTC().Format(time.RFC3339)
}

func (s *State) update(url string, change func(*entry)) error {
	e := s.data.Reviews[url]
	change(&e)
	s.data.Reviews[url] = e
	return s.save()
}

func (s *State) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, s.path)
}
