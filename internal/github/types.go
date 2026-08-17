package github

import "time"

// IssueRef identifies a single GitHub issue by owner/repo/number.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
}

// Issue is the subset of the GitHub Issues API response ContriAI needs.
type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	Labels    []Label   `json:"labels"`
	User      User      `json:"user"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"created_at"`
}

type Label struct {
	Name string `json:"name"`
}

type User struct {
	Login string `json:"login"`
}

// Comment is a single issue comment.
type Comment struct {
	Body string `json:"body"`
	User User   `json:"user"`
}

// Repository is the subset of the GitHub Repos API response ContriAI needs.
type Repository struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Language      string `json:"language"`
	StargazersCnt int    `json:"stargazers_count"`
	OpenIssues    int    `json:"open_issues_count"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// TreeEntry is one file/dir entry from the git trees API.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	Size int    `json:"size"`
}

type treeResponse struct {
	Tree      []TreeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}
