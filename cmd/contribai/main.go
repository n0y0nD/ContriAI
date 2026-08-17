// Command contribai runs ContriAI — a local web UI + API that helps
// developers understand GitHub issues and explore repositories before
// contributing. See README.md for setup and the full project roadmap.
//
// Phase 1: issue analyzer (fetch issue + repo file tree, ask an LLM to explain it).
// Phase 2: repository navigator (download source, grep-search it, answer
//           free-form questions grounded in the actual code).
package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"contribai/internal/ai"
	"contribai/internal/analysis"
	gh "contribai/internal/github"
	"contribai/internal/navigator"
)

//go:embed web/*
var webFS embed.FS

func main() {
	addr := envOr("CONTRIBAI_ADDR", ":8080")
	githubToken := os.Getenv("GITHUB_TOKEN")

	provider := selectProvider()
	analyzer := analysis.New(gh.NewClient(githubToken), provider)
	nav := navigator.New(provider, githubToken)

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embedding web assets: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/analyze", analyzeHandler(analyzer))
	mux.HandleFunc("/api/ask", askHandler(nav))
	mux.HandleFunc("/api/health", healthHandler(provider))

	log.Printf("ContriAI listening on http://localhost%s  (AI provider: %s)", addr, provider.Name())
	if provider.Name() == "mock (no LLM configured)" {
		log.Printf("Tip: install Ollama and run `ollama pull llama3.2`, then set CONTRIBAI_PROVIDER=ollama for real analysis.")
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

func selectProvider() ai.Provider {
	switch envOr("CONTRIBAI_PROVIDER", "mock") {
	case "ollama":
		model := envOr("CONTRIBAI_OLLAMA_MODEL", "llama3.2")
		baseURL := os.Getenv("CONTRIBAI_OLLAMA_URL")
		return ai.NewOllamaProvider(baseURL, model)
	default:
		return ai.NewMockProvider()
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Phase 1: issue analysis ────────────────────────────────────────────────

type analyzeRequest struct {
	IssueURL string `json:"issue_url"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func analyzeHandler(analyzer *analysis.Analyzer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(errorResponse{Error: "use POST"})
			return
		}

		var req analyzeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		result, err := analyzer.Analyze(ctx, req.IssueURL)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(errorResponse{Error: err.Error()})
			return
		}

		json.NewEncoder(w).Encode(result)
	}
}

// ── Phase 2: repository navigator ─────────────────────────────────────────

type askRequest struct {
	// RepoURL accepts either a full GitHub URL (https://github.com/owner/repo)
	// or a short "owner/repo" form.
	RepoURL  string `json:"repo_url"`
	Question string `json:"question"`
}

// parseOwnerRepo parses "owner/repo" from either a full GitHub URL or a
// bare "owner/repo" string.
func parseOwnerRepo(raw string) (owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	// Full URL: https://github.com/owner/repo[/...]
	raw = strings.TrimPrefix(raw, "https://github.com/")
	raw = strings.TrimPrefix(raw, "http://github.com/")
	raw = strings.TrimPrefix(raw, "github.com/")

	parts := strings.SplitN(raw, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	// Strip trailing .git if present.
	repoName := strings.TrimSuffix(parts[1], ".git")
	return parts[0], repoName, true
}

func askHandler(nav *navigator.Navigator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(errorResponse{Error: "use POST"})
			return
		}

		var req askRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
			return
		}

		owner, repo, ok := parseOwnerRepo(req.RepoURL)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errorResponse{Error: "repo_url must be a GitHub repo URL or 'owner/repo'"})
			return
		}
		if strings.TrimSpace(req.Question) == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errorResponse{Error: "question is required"})
			return
		}

		// Generous timeout: downloading a tarball + LLM call.
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		resp, err := nav.Ask(ctx, navigator.Request{
			Owner:    owner,
			Repo:     repo,
			Question: req.Question,
		})
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(errorResponse{Error: err.Error()})
			return
		}

		json.NewEncoder(w).Encode(resp)
	}
}

// ── Health ────────────────────────────────────────────────────────────────

func healthHandler(provider ai.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":   "ok",
			"provider": provider.Name(),
		})
	}
}
