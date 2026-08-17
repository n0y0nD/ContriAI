// Package analysis orchestrates ContriAI's Phase 1 workflow: given a GitHub
// issue URL, gather enough repository context to be useful, hand it to an
// ai.Provider, and turn the response into a structured Result the frontend
// can render as cards instead of a wall of chat text.
package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"contribai/internal/ai"
	gh "contribai/internal/github"
)

// Section markers shared with internal/ai's Mock provider so it can parse
// the same prompt this file builds. Keep these in sync if either changes.
const (
	markerIssueTitle = "ISSUE TITLE:"
	markerIssueBody  = "ISSUE BODY:"
	markerRepo       = "REPOSITORY:"
	markerFileTree   = "FILE TREE:"
)

// maxTreeEntries bounds how many file paths get sent to the model. Large
// monorepos would otherwise blow the context window for no real benefit —
// Phase 1 relies on filename relevance, not full source, per the roadmap's
// "don't build the RAG pipeline yet" guidance.
const maxTreeEntries = 300

// Analyzer wires a GitHub client to an AI provider to produce Results.
type Analyzer struct {
	GitHub   *gh.Client
	Provider ai.Provider
}

// New builds an Analyzer.
func New(githubClient *gh.Client, provider ai.Provider) *Analyzer {
	return &Analyzer{GitHub: githubClient, Provider: provider}
}

// Analyze runs the full Phase 1 pipeline for a single issue URL.
func (a *Analyzer) Analyze(ctx context.Context, issueURL string) (*Result, error) {
	ref, err := gh.ParseIssueURL(issueURL)
	if err != nil {
		return nil, err
	}

	issue, err := a.GitHub.FetchIssue(ref)
	if err != nil {
		return nil, fmt.Errorf("fetching issue: %w", err)
	}

	repo, err := a.GitHub.FetchRepository(ref.Owner, ref.Repo)
	if err != nil {
		return nil, fmt.Errorf("fetching repository: %w", err)
	}

	branch := repo.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	tree, truncated, err := a.GitHub.FetchTree(ref.Owner, ref.Repo, branch, maxTreeEntries)
	if err != nil {
		// A missing/renamed tree shouldn't kill the whole analysis — the
		// model can still reason from the issue text alone, just with a
		// weaker "relevant files" section.
		tree = nil
	}

	prompt := buildPrompt(issue, repo, tree, truncated)

	raw, err := a.Provider.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI provider (%s): %w", a.Provider.Name(), err)
	}

	result := parseModelOutput(raw)
	result.IssueTitle = issue.Title
	result.IssueURL = issue.HTMLURL
	result.Repository = repo.FullName
	result.Provider = a.Provider.Name()
	return result, nil
}

func buildPrompt(issue *gh.Issue, repo *gh.Repository, tree []gh.TreeEntry, truncated bool) string {
	var b strings.Builder

	b.WriteString("You are ContriAI, an assistant that helps a developer understand a GitHub issue well enough to start contributing. ")
	b.WriteString("You do not write the patch yourself — you explain the issue, point at likely files, and outline an approach for a human to follow.\n\n")

	fmt.Fprintf(&b, "%s %s\n", markerIssueTitle, issue.Title)
	fmt.Fprintf(&b, "%s\n%s\n\n", markerIssueBody, orPlaceholder(issue.Body, "(no description provided)"))
	fmt.Fprintf(&b, "%s %s — %s (primary language: %s)\n\n", markerRepo, repo.FullName, orPlaceholder(repo.Description, "no description"), orPlaceholder(repo.Language, "unknown"))

	b.WriteString(markerFileTree + "\n")
	if len(tree) == 0 {
		b.WriteString("(unavailable)\n")
	} else {
		for _, e := range tree {
			if e.Type == "blob" {
				b.WriteString("- " + e.Path + "\n")
			}
		}
		if truncated {
			b.WriteString("(list truncated — repository is larger than shown)\n")
		}
	}

	b.WriteString("\nRespond with ONLY a JSON object (no markdown fences, no commentary) matching exactly this shape:\n")
	b.WriteString(`{"understanding":"2-4 sentences on what the issue is actually asking for","relevant_files":["path/one","path/two"],"approach":["step 1","step 2","step 3"],"risks":["risk 1","risk 2"]}`)
	b.WriteString("\nPick relevant_files ONLY from the file tree above. If nothing looks relevant, return an empty list rather than guessing a path that wasn't shown.\n")

	return b.String()
}

func orPlaceholder(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

// parseModelOutput extracts the JSON object the prompt asked for, even if
// the model wrapped it in prose or a markdown fence. If nothing parseable
// is found, the raw response is preserved as the "understanding" text so
// the user still sees something useful instead of an error.
func parseModelOutput(raw string) *Result {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")

	if start == -1 || end == -1 || end < start {
		return &Result{Understanding: strings.TrimSpace(raw)}
	}

	var parsed rawModelOutput
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return &Result{Understanding: strings.TrimSpace(raw)}
	}

	return &Result{
		Understanding: parsed.Understanding,
		RelevantFiles: parsed.RelevantFiles,
		Approach:      parsed.Approach,
		Risks:         parsed.Risks,
	}
}
