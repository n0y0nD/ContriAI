package ai

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// Section markers must match internal/analysis's prompt builder. Duplicated
// here (rather than imported) to avoid an import cycle between ai and
// analysis — ai is the lower-level package.
const (
	mockMarkerIssueTitle = "ISSUE TITLE:"
	mockMarkerIssueBody  = "ISSUE BODY:"
	mockMarkerRepo       = "REPOSITORY:"
	mockMarkerFileTree   = "FILE TREE:"
)

// MockProvider is a zero-dependency stand-in for a real LLM. It can't
// reason about code the way a model can, but it does real work — keyword
// overlap between the issue text and the repository's file tree — so a
// developer with no local model installed still gets a usable first pass,
// and the whole pipeline (fetch → prompt → parse → render) is exercisable
// without any external AI service.
type MockProvider struct{}

func NewMockProvider() *MockProvider { return &MockProvider{} }

func (p *MockProvider) Name() string { return "mock (no LLM configured)" }

func (p *MockProvider) Complete(_ context.Context, prompt string) (string, error) {
	title := extractBetween(prompt, mockMarkerIssueTitle, mockMarkerIssueBody)
	body := extractBetween(prompt, mockMarkerIssueBody, mockMarkerRepo)
	files := extractFiles(prompt)

	keywords := tokenize(title + " " + body)
	ranked := rankFiles(files, keywords)

	top := ranked
	if len(top) > 5 {
		top = top[:5]
	}
	var relevant []string
	for _, f := range top {
		relevant = append(relevant, f.path)
	}

	understanding := "Heuristic pass only (no LLM configured): this ranks files by keyword overlap with the issue text, it doesn't read code. "
	if len(relevant) > 0 {
		understanding += "The files below share the most vocabulary with the issue title/body, so they're a reasonable place to start reading."
	} else {
		understanding += "No file names shared meaningful vocabulary with the issue text — start from the repository's top-level README instead."
	}

	out := struct {
		Understanding string   `json:"understanding"`
		RelevantFiles []string `json:"relevant_files"`
		Approach      []string `json:"approach"`
		Risks         []string `json:"risks"`
	}{
		Understanding: understanding,
		RelevantFiles: relevant,
		Approach: []string{
			"Read the issue thread in full, including linked issues/PRs, before touching code.",
			"Open the ranked files above and search the repo for the issue's key terms to confirm they're actually relevant.",
			"Write or run the existing tests for that area first, so you know what passing looks like before you change anything.",
			"Make the smallest change that addresses the issue, then re-run those tests.",
		},
		Risks: []string{
			"This ranking is keyword-based, not semantic — connect a real model (Ollama) for actual code-aware analysis.",
			"Confirm the default branch and file paths still exist; the tree snapshot can be stale for fast-moving repos.",
		},
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func extractBetween(s, startMarker, endMarker string) string {
	start := strings.Index(s, startMarker)
	if start == -1 {
		return ""
	}
	start += len(startMarker)
	rest := s[start:]
	end := strings.Index(rest, endMarker)
	if end == -1 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

func extractFiles(prompt string) []string {
	idx := strings.Index(prompt, mockMarkerFileTree)
	if idx == -1 {
		return nil
	}
	var files []string
	for _, line := range strings.Split(prompt[idx:], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			files = append(files, strings.TrimPrefix(line, "- "))
		}
	}
	return files
}

func tokenize(s string) map[string]bool {
	set := map[string]bool{}
	for _, raw := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
	}) {
		if len(raw) > 3 { // skip short/noisy tokens like "the", "add", "a"
			set[raw] = true
		}
	}
	return set
}

type scoredFile struct {
	path  string
	score int
}

func rankFiles(files []string, keywords map[string]bool) []scoredFile {
	var scored []scoredFile
	for _, f := range files {
		tokens := tokenize(strings.NewReplacer("/", " ", "_", " ", "-", " ", ".", " ").Replace(f))
		score := 0
		for t := range tokens {
			if keywords[t] {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredFile{path: f, score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].path < scored[j].path
	})
	return scored
}
