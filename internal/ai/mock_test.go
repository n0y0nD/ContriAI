package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMockProviderRanksRelevantFiles(t *testing.T) {
	prompt := strings.Join([]string{
		"ISSUE TITLE: Add XYZ support to the authentication system",
		"ISSUE BODY:",
		"We need the authenticate flow to recognize XYZ credentials in the token validator.",
		"REPOSITORY: example/repo — a web server (primary language: Go)",
		"",
		"FILE TREE:",
		"- auth/middleware.go",
		"- auth/token.go",
		"- server/routes.go",
		"- docs/README.md",
		"- vendor/unrelated/thing.go",
	}, "\n")

	p := NewMockProvider()
	raw, err := p.Complete(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}

	var out struct {
		Understanding string   `json:"understanding"`
		RelevantFiles []string `json:"relevant_files"`
		Approach      []string `json:"approach"`
		Risks         []string `json:"risks"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("mock provider did not return valid JSON: %v\nraw: %s", err, raw)
	}

	if len(out.RelevantFiles) == 0 {
		t.Fatalf("expected at least one relevant file to be ranked, got none")
	}
	if out.RelevantFiles[0] != "auth/token.go" && out.RelevantFiles[0] != "auth/middleware.go" {
		t.Errorf("expected an auth/* file to rank first for an authentication issue, got %v", out.RelevantFiles)
	}
	for _, f := range out.RelevantFiles {
		if f == "vendor/unrelated/thing.go" {
			t.Errorf("unrelated file should not have been ranked as relevant: %v", out.RelevantFiles)
		}
	}
	if len(out.Approach) == 0 {
		t.Error("expected non-empty approach steps")
	}
}

func TestMockProviderHandlesNoMatches(t *testing.T) {
	prompt := strings.Join([]string{
		"ISSUE TITLE: zzz qqq",
		"ISSUE BODY:",
		"nothing that matches any filename here",
		"REPOSITORY: example/repo — desc (primary language: Go)",
		"",
		"FILE TREE:",
		"- main.go",
	}, "\n")

	p := NewMockProvider()
	raw, err := p.Complete(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	var out struct {
		RelevantFiles []string `json:"relevant_files"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(out.RelevantFiles) != 0 {
		t.Errorf("expected no relevant files for non-matching keywords, got %v", out.RelevantFiles)
	}
}
