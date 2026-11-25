package models

import (
	"encoding/json"
	"testing"
)

func TestChangedFilePathsDeduplicates(t *testing.T) {
	ctx := PRContext{
		Files: []FileChange{
			{Path: "a.go"},
			{Path: "b.go"},
			{Path: "a.go"},
		},
	}

	got := ctx.ChangedFilePaths()

	if len(got) != 2 {
		t.Fatalf("ChangedFilePaths() returned %d paths, want 2", len(got))
	}
	for _, want := range []string{"a.go", "b.go"} {
		if _, ok := got[want]; !ok {
			t.Errorf("ChangedFilePaths() missing %q", want)
		}
	}
}

func TestChangedFilePathsEmpty(t *testing.T) {
	ctx := PRContext{}
	if got := ctx.ChangedFilePaths(); len(got) != 0 {
		t.Errorf("ChangedFilePaths() on empty context returned %d paths, want 0", len(got))
	}
}

func TestTotalsSumFiles(t *testing.T) {
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
