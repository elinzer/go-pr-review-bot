package models

import "testing"

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

func TestSeverityValid(t *testing.T) {
	tests := []struct {
		severity Severity
		want     bool
	}{
		{SeverityBug, true},
		{SeverityQuestion, true},
		{Severity("style"), false},
		{Severity(""), false},
	}
	for _, tt := range tests {
		if got := tt.severity.Valid(); got != tt.want {
			t.Errorf("Severity(%q).Valid() = %v, want %v", tt.severity, got, tt.want)
		}
	}
}
