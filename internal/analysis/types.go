package analysis

// Result is the structured output ContriAI shows for an issue. It maps
// directly onto the "Issue Analysis Page" sections from the project spec:
// understanding, relevant files/functions, suggested approach, risks.
type Result struct {
	IssueTitle      string           `json:"issue_title"`
	IssueURL        string           `json:"issue_url"`
	Repository      string           `json:"repository"`
	Understanding   string           `json:"understanding"`
	RelevantFiles   []string         `json:"relevant_files"`
	Approach        []string         `json:"approach"`
	Risks           []string         `json:"risks"`
	RetrievedChunks []RetrievedChunk `json:"retrieved_chunks"`
	Provider        string           `json:"provider"`
}

// RetrievedChunk identifies an exact source range used for issue analysis.
type RetrievedChunk struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// rawModelOutput is the JSON shape the AI provider is asked to return. It's
// kept separate from Result so a provider that returns slightly malformed
// JSON (missing a field, wrong casing) can still be salvaged field-by-field
// in issue.go rather than failing the whole analysis.
type rawModelOutput struct {
	Understanding string   `json:"understanding"`
	RelevantFiles []string `json:"relevant_files"`
	Approach      []string `json:"approach"`
	Risks         []string `json:"risks"`
}
