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
	Path      string
	Patch     string
	Additions int
	Deletions int
}

type PRContext struct {
	Summary PullRequestSummary
	Files   []FileChange
}

func (c PRContext) ChangedFilePaths() map[string]struct{} {
	paths := make(map[string]struct{}, len(c.Files))
	for _, f := range c.Files {
		paths[f.Path] = struct{}{}
	}
	return paths
}

type JiraContext struct {
	Key                string
	Title              string
	Description        string
	AcceptanceCriteria string
}

type Evidence struct {
	QuotedCode string `json:"quoted_code"`
	Citation   string `json:"citation"`
}

type Severity string

const (
	SeverityBug      Severity = "bug"
	SeverityQuestion Severity = "question"
)

func (s Severity) Valid() bool {
	return s == SeverityBug || s == SeverityQuestion
}

type Comment struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Body     string   `json:"body"`
	Evidence Evidence `json:"evidence"`
}

type Review struct {
	Summary  string    `json:"summary"`
	Comments []Comment `json:"comments"`
}
