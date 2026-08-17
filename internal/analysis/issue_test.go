package analysis

import (
	"strings"
	"testing"

	gh "contribai/internal/github"
)

func TestBuildPromptIncludesCoreSections(t *testing.T) {
	issue := &gh.Issue{Title: "Fix flaky test", Body: "The test times out randomly."}
	repo := &gh.Repository{FullName: "example/repo", Description: "demo", Language: "Go"}
	tree := []gh.TreeEntry{
		{Path: "internal/runner/runner.go", Type: "blob"},
		{Path: "internal/runner", Type: "tree"}, // dirs should be skipped
	}

	prompt := buildPrompt(issue, repo, tree, false)

	for _, want := range []string{
		"ISSUE TITLE: Fix flaky test",
		"The test times out randomly.",
		"example/repo",
		"internal/runner/runner.go",
		"JSON object",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "- internal/runner\n") {
		t.Errorf("directory entries should not be listed as files")
	}
}

func TestParseModelOutputValidJSON(t *testing.T) {
	raw := `{"understanding":"it's about X","relevant_files":["a.go"],"approach":["step1"],"risks":["risk1"]}`
	r := parseModelOutput(raw)
	if r.Understanding != "it's about X" {
		t.Errorf("understanding mismatch: %q", r.Understanding)
	}
	if len(r.RelevantFiles) != 1 || r.RelevantFiles[0] != "a.go" {
		t.Errorf("relevant_files mismatch: %v", r.RelevantFiles)
	}
}

func TestParseModelOutputHandlesFencedJSON(t *testing.T) {
	raw := "Sure, here you go:\n```json\n{\"understanding\":\"ok\",\"relevant_files\":[],\"approach\":[],\"risks\":[]}\n```\nHope that helps!"
	r := parseModelOutput(raw)
	if r.Understanding != "ok" {
		t.Errorf("expected JSON to be extracted from fenced/prose response, got understanding=%q", r.Understanding)
	}
}

func TestParseModelOutputFallsBackOnGarbage(t *testing.T) {
	raw := "the model just rambled with no JSON at all"
	r := parseModelOutput(raw)
	if r.Understanding != raw {
		t.Errorf("expected raw text fallback, got %q", r.Understanding)
	}
	if r.RelevantFiles != nil {
		t.Errorf("expected nil relevant files on fallback, got %v", r.RelevantFiles)
	}
}
