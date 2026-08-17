package analysis

// Result is the structured output ContriAI shows for an issue. It maps
// directly onto the "Issue Analysis Page" sections from the project spec:
// understanding, relevant files/functions, suggested approach, risks.
type Result struct {
	IssueTitle    string   `json:"issue_title"`
	IssueURL      string   `json:"issue_url"`
	Repository    string   `json:"repository"`
	Understanding string   `json:"understanding"`
	RelevantFiles []string `json:"relevant_files"`
	Approach      []string `json:"approach"`
	Risks         []string `json:"risks"`
	Provider      string   `json:"provider"`
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
