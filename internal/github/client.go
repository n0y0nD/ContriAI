// Package github provides a minimal, read-only client for the parts of the
// GitHub REST API that ContriAI's Phase 1 issue analyzer needs: fetching an
// issue, its comments, the parent repository, and a shallow file tree.
package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.github.com"

var issueURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/issues/(\d+)/?$`)

// Client is a small wrapper around net/http configured for the GitHub REST API.
type Client struct {
	httpClient *http.Client
	token      string // optional; raises the unauthenticated rate limit when set
	apiBase    string // overridable in tests to point at a fake server
}

// NewClient builds a Client. token may be empty for unauthenticated (rate
// limited) access, or a GitHub personal access token for higher limits and
// access to private repositories.
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		token:      token,
		apiBase:    defaultAPIBase,
	}
}

// NewClientWithBaseURL builds a Client against a custom API base URL —
// intended for tests that stand up a fake GitHub server.
func NewClientWithBaseURL(token, baseURL string) *Client {
	c := NewClient(token)
	c.apiBase = baseURL
	return c
}

// ParseIssueURL extracts owner/repo/number from a GitHub issue URL such as
// https://github.com/podman-container-tools/podman/issues/29265
func ParseIssueURL(raw string) (IssueRef, error) {
	m := issueURLPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return IssueRef{}, fmt.Errorf("not a recognizable GitHub issue URL: %q", raw)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return IssueRef{}, fmt.Errorf("invalid issue number in URL: %w", err)
	}
	return IssueRef{Owner: m[1], Repo: m[2], Number: n}, nil
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "contribai/0.1 (+phase1 issue analyzer)")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", req.URL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s: %w", req.URL, err)
	}

	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
		return fmt.Errorf("GitHub API rate limit hit; set a GITHUB_TOKEN to raise the limit")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API %s returned %d: %s", req.URL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", req.URL, err)
	}
	return nil
}

func (c *Client) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.apiBase+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// FetchIssue retrieves a single issue.
func (c *Client) FetchIssue(ref IssueRef) (*Issue, error) {
	var issue Issue
	path := fmt.Sprintf("/repos/%s/%s/issues/%d",
		url.PathEscape(ref.Owner), url.PathEscape(ref.Repo), ref.Number)
	if err := c.get(path, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// FetchComments retrieves the comments on an issue (capped at `limit`).
func (c *Client) FetchComments(ref IssueRef, limit int) ([]Comment, error) {
	var comments []Comment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d",
		url.PathEscape(ref.Owner), url.PathEscape(ref.Repo), ref.Number, limit)
	if err := c.get(path, &comments); err != nil {
		return nil, err
	}
	if len(comments) > limit {
		comments = comments[:limit]
	}
	return comments, nil
}

// FetchRepository retrieves repository metadata.
func (c *Client) FetchRepository(owner, repo string) (*Repository, error) {
	var r Repository
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	if err := c.get(path, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// FetchTree retrieves the repository's file tree (recursively) for the given
// branch, capped at `limit` entries so huge monorepos don't blow up the
// context sent to the AI provider.
func (c *Client) FetchTree(owner, repo, branch string, limit int) ([]TreeEntry, bool, error) {
	var tr treeResponse
	path := fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	if err := c.get(path, &tr); err != nil {
		return nil, false, err
	}
	entries := tr.Tree
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, tr.Truncated, nil
}
