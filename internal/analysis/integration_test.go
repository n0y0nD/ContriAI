package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"contribai/internal/ai"
	gh "contribai/internal/github"
	"contribai/internal/rag"
)

type fakeRetriever struct{}

func (fakeRetriever) Retrieve(_ context.Context, _, _, _, _ string) ([]rag.Chunk, error) {
	return []rag.Chunk{{
		Path: "auth/token.go", StartLine: 1, EndLine: 4, Score: 0.82,
		Content: "package auth\nfunc ValidateToken(token string) bool { return token != \"\" }",
	}}, nil
}

// TestAnalyzeEndToEnd exercises the full Phase 1 pipeline — issue fetch,
// repo fetch, tree fetch, prompt build, mock AI provider, response parsing
// — against a fake GitHub server, so it runs offline and deterministically.
func TestAnalyzeEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/example/repo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"title":    "Authentication token validation fails randomly",
			"body":     "The token validator sometimes rejects valid XYZ tokens.",
			"html_url": "https://github.com/example/repo/issues/42",
			"state":    "open",
		})
	})
	mux.HandleFunc("/repos/example/repo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"full_name":      "example/repo",
			"description":    "an example server",
			"language":       "Go",
			"default_branch": "main",
		})
	})
	mux.HandleFunc("/repos/example/repo/git/trees/main", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"truncated": false,
			"tree": []map[string]any{
				{"path": "auth/token.go", "type": "blob"},
				{"path": "auth/middleware.go", "type": "blob"},
				{"path": "docs/README.md", "type": "blob"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := gh.NewClientWithBaseURL("", server.URL)
	analyzer := New(client, ai.NewMockProvider(), fakeRetriever{})

	result, err := analyzer.Analyze(context.Background(), "https://github.com/example/repo/issues/42")
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if result.Repository != "example/repo" {
		t.Errorf("expected repository example/repo, got %q", result.Repository)
	}
	if !strings.Contains(result.IssueTitle, "Authentication") {
		t.Errorf("expected issue title to be populated, got %q", result.IssueTitle)
	}
	if len(result.RelevantFiles) == 0 {
		t.Fatalf("expected at least one relevant file from the fake tree, got none")
	}
	found := false
	for _, f := range result.RelevantFiles {
		if strings.HasPrefix(f, "auth/") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an auth/* file to be ranked relevant, got %v", result.RelevantFiles)
	}
	if len(result.Approach) == 0 {
		t.Error("expected non-empty approach steps")
	}
	if result.Provider == "" {
		t.Error("expected provider name to be set")
	}
	if len(result.RetrievedChunks) != 1 || result.RetrievedChunks[0].Path != "auth/token.go" {
		t.Errorf("expected returned RAG source evidence, got %v", result.RetrievedChunks)
	}
}
