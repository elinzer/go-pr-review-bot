package models

type PullRequestSummary struct {
	URL          string
	RepoFullName string
	Number       int
	Title        string
	HeadSHA      string
	Body         string
	Branch       string
}

type FileChange struct {
	Path         string
	Patch        string
	Additions    int
	Deletions    int
	PreviousPath string
	Status       string
	FullContent  string
}

type PRContext struct {
	Summary      PullRequestSummary
	Files        []FileChange
	OtherChanges []string
}

func (c PRContext) Totals() (additions, deletions int) {
	for _, f := range c.Files {
		additions += f.Additions
		deletions += f.Deletions
	}
	return additions, deletions
}

func (c PRContext) ChangedFilePaths() map[string]struct{} {
	paths := make(map[string]struct{}, len(c.Files))
	for _, f := range c.Files {
		paths[f.Path] = struct{}{}
	}
	return paths
}

type JiraContext struct {
	Key          string
	Title        string
	Description  string
	Epic         string
	LinkedIssues []string
}

type AlignmentStatus string

const (
	AlignmentMet     AlignmentStatus = "met"
	AlignmentGap     AlignmentStatus = "gap"
	AlignmentExtra   AlignmentStatus = "extra"
	AlignmentUnclear AlignmentStatus = "unclear"
)

func (s AlignmentStatus) Valid() bool {
	switch s {
	case AlignmentMet, AlignmentGap, AlignmentExtra, AlignmentUnclear:
		return true
	default:
		return false
	}
}

type Alignment struct {
	Status AlignmentStatus `json:"status"`
	Note   string          `json:"note"`
}

type LookItem struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Why  string `json:"why"`
}

type Briefing struct {
	TicketSummary   string      `json:"ticket_summary"`
	PRSummary       string      `json:"pr_summary"`
	TicketAlignment []Alignment `json:"ticket_alignment"`
	WhereToLook     []LookItem  `json:"where_to_look"`
}

type Usage struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}
