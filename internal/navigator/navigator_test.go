package navigator

import (
	"archive/tar"
	"compress/gzip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// Helpers shared across tests
// ──────────────────────────────────────────────────────────────────────────────

// fakeProvider is a deterministic ai.Provider that echoes back a valid
// navigator JSON response using only files it sees in the prompt.
type fakeProvider struct{}

func (f *fakeProvider) Name() string { return "fake-navigator" }

func (f *fakeProvider) Complete(_ context.Context, prompt string) (string, error) {
	// Extract file paths the prompt listed under "--- FILE: ... ---" markers.
	var cited []string
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- FILE:") && strings.HasSuffix(line, "---") {
			path := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "--- FILE:"), "---"))
			if path != "" {
				cited = append(cited, path)
			}
		}
	}
	out := rawAskOutput{
		Answer:     "Fake answer grounded in context.",
		CitedFiles: cited,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// buildTestTarGz creates an in-memory .tar.gz archive that wraps all the given
// files under a top-level "myrepo-abc123/" prefix (matching GitHub's real
// tarball layout) so the navigator's strip logic is exercised.
func buildTestTarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Add top-level directory that GitHub wraps around the tree.
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "myrepo-abc123/",
		Mode:     0o755,
	})

	for name, content := range files {
		body := []byte(content)
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "myrepo-abc123/" + name,
			Size:     int64(len(body)),
			Mode:     0o644,
		})
		tw.Write(body)
	}

	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serveFakeTarball starts an httptest server that serves a fake tarball (and
// optionally follows a redirect like GitHub does).
func serveFakeTarball(tarGz []byte) *httptest.Server {
	mux := http.NewServeMux()
	// GitHub API redirects /repos/:owner/:repo/tarball/:ref → CDN URL.
	mux.HandleFunc("/repos/testowner/testrepo/tarball/HEAD", func(w http.ResponseWriter, r *http.Request) {
		// Serve directly (skip redirect for simplicity in tests).
		w.Header().Set("Content-Type", "application/x-gzip")
		w.WriteHeader(http.StatusOK)
		w.Write(tarGz)
	})
	return httptest.NewServer(mux)
}

// ──────────────────────────────────────────────────────────────────────────────
// Unit tests — extractKeywords
// ──────────────────────────────────────────────────────────────────────────────

func TestExtractKeywords(t *testing.T) {
	kws := extractKeywords("Where is authentication implemented?")
	if len(kws) == 0 {
		t.Fatal("expected keywords, got none")
	}
	for _, kw := range kws {
		if stopWords[kw] {
			t.Errorf("keyword %q is a stop word, should have been filtered", kw)
		}
	}
	// "authentication" should be present (not a stop word, > 2 chars).
	found := false
	for _, kw := range kws {
		if kw == "authentication" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'authentication' in keywords, got %v", kws)
	}
}

func TestExtractKeywordsDedup(t *testing.T) {
	kws := extractKeywords("auth auth auth login login")
	count := 0
	for _, kw := range kws {
		if kw == "auth" {
			count++
		}
	}
	if count > 1 {
		t.Errorf("expected 'auth' to appear once, got %d times", count)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Unit tests — searchRepo
// ──────────────────────────────────────────────────────────────────────────────

func makeTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestSearchRepo_FilenameMatch(t *testing.T) {
	dir := makeTestRepo(t, map[string]string{
		"auth/token.go":    "package auth\n\nfunc ValidateToken(t string) bool { return t != \"\" }\n",
		"server/http.go":   "package server\n\nfunc Start() {}\n",
		"docs/overview.md": "# Overview\n",
	})

	results := searchRepo(dir, "Where is token validation?")
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	// auth/token.go scores on both "token" in path and content.
	if results[0].Path != "auth/token.go" {
		t.Errorf("expected auth/token.go as top result, got %q", results[0].Path)
	}
}

func TestSearchRepo_ContentMatch(t *testing.T) {
	dir := makeTestRepo(t, map[string]string{
		"cmd/cli.go": "package main\n\nvar FlagVerbose bool\nfunc parseFlags() { /* ... */ }\n",
		"main.go":    "package main\n\nfunc main() { parseFlags() }\n",
	})

	results := searchRepo(dir, "Which files handle CLI flag parsing?")
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	var paths []string
	for _, r := range results {
		paths = append(paths, r.Path)
	}
	found := false
	for _, p := range paths {
		if strings.Contains(p, "cli") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cmd/cli.go in results, got %v", paths)
	}
}

func TestSearchRepo_NoMatch(t *testing.T) {
	dir := makeTestRepo(t, map[string]string{
		"hello.go": "package main\nfunc main() {}\n",
	})
	results := searchRepo(dir, "xyzzy cryptographic blockchain nonce")
	// May or may not match — what we care about is it doesn't crash.
	_ = results
}

// ──────────────────────────────────────────────────────────────────────────────
// Unit tests — parseAskOutput
// ──────────────────────────────────────────────────────────────────────────────

func TestParseAskOutput_CleanJSON(t *testing.T) {
	raw := `{"answer":"Auth is in auth/token.go","cited_files":["auth/token.go"]}`
	answer, cited := parseAskOutput(raw)
	if answer != "Auth is in auth/token.go" {
		t.Errorf("unexpected answer: %q", answer)
	}
	if len(cited) != 1 || cited[0] != "auth/token.go" {
		t.Errorf("unexpected cited: %v", cited)
	}
}

func TestParseAskOutput_WrappedInProse(t *testing.T) {
	raw := `Sure! Here you go: {"answer":"yes","cited_files":["foo.go"]} Hope that helps.`
	answer, cited := parseAskOutput(raw)
	if answer != "yes" {
		t.Errorf("expected answer 'yes', got %q", answer)
	}
	if len(cited) != 1 || cited[0] != "foo.go" {
		t.Errorf("expected cited [foo.go], got %v", cited)
	}
}

func TestParseAskOutput_Fallback(t *testing.T) {
	raw := "Sorry, I cannot determine that from the context."
	answer, cited := parseAskOutput(raw)
	if answer == "" {
		t.Error("expected non-empty fallback answer")
	}
	if len(cited) != 0 {
		t.Errorf("expected no cited files in fallback, got %v", cited)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Unit tests — extractTarGz
// ──────────────────────────────────────────────────────────────────────────────

func TestExtractTarGz(t *testing.T) {
	tarGz := buildTestTarGz(map[string]string{
		"auth/token.go": "package auth",
		"main.go":       "package main",
	})

	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(tarGz), dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// After extraction the files are under dest/myrepo-abc123/.
	want := filepath.Join(dest, "myrepo-abc123", "auth", "token.go")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected %s to exist: %v", want, err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Integration test — Navigator.Ask against a fake HTTP server
// ──────────────────────────────────────────────────────────────────────────────

// TestAskEndToEnd exercises the full Phase 2 pipeline:
//   - fake GitHub tarball server (no real network)
//   - tarball contains real file content
//   - keyword search finds relevant files
//   - fake AI provider returns a grounded answer
//
// This mirrors the pattern used in internal/analysis/integration_test.go.
func TestAskEndToEnd(t *testing.T) {
	tarGz := buildTestTarGz(map[string]string{
		"auth/token.go": `package auth

// ValidateToken checks whether a bearer token is valid.
func ValidateToken(token string) bool {
	return len(token) > 0 && token != "anonymous"
}
`,
		"auth/middleware.go": `package auth

import "net/http"

// Middleware wraps an HTTP handler and enforces token authentication.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		if !ValidateToken(tok) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
`,
		"server/server.go": `package server

func Start(addr string) error { return nil }
`,
	})

	srv := serveFakeTarball(tarGz)
	defer srv.Close()

	nav := newWithBase(&fakeProvider{}, "", srv.URL, srv.Client())

	resp, err := nav.Ask(context.Background(), Request{
		Owner:    "testowner",
		Repo:     "testrepo",
		Question: "Where is authentication and token validation implemented?",
	})
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}

	if resp.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if len(resp.CitedFiles) == 0 {
		t.Error("expected at least one cited file")
	}

	// At least one auth file should be cited.
	authFound := false
	for _, f := range resp.CitedFiles {
		if strings.HasPrefix(f, "auth/") {
			authFound = true
		}
	}
	if !authFound {
		t.Errorf("expected an auth/* file in cited_files, got %v", resp.CitedFiles)
	}

	if resp.Provider != "fake-navigator" {
		t.Errorf("unexpected provider name: %q", resp.Provider)
	}
}

// TestAskEndToEnd_NoKeywordMatch verifies the navigator still returns a
// response even when no files match the query keywords.
func TestAskEndToEnd_NoKeywordMatch(t *testing.T) {
	tarGz := buildTestTarGz(map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})

	srv := serveFakeTarball(tarGz)
	defer srv.Close()

	nav := newWithBase(&fakeProvider{}, "", srv.URL, srv.Client())

	resp, err := nav.Ask(context.Background(), Request{
		Owner:    "testowner",
		Repo:     "testrepo",
		Question: "xyzzy cryptographic blockchain nonce algorithm",
	})
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	// Answer should be non-empty (even if it says it couldn't find anything).
	if resp.Answer == "" {
		t.Error("expected non-empty answer even when no files match")
	}
}

// TestAskMissingOwnerRepo verifies validation rejects empty owner/repo.
func TestAskMissingOwnerRepo(t *testing.T) {
	nav := New(&fakeProvider{}, "")
	_, err := nav.Ask(context.Background(), Request{Question: "anything"})
	if err == nil {
		t.Error("expected error for missing owner/repo")
	}
}

// TestAskMissingQuestion verifies validation rejects empty questions.
func TestAskMissingQuestion(t *testing.T) {
	nav := New(&fakeProvider{}, "")
	_, err := nav.Ask(context.Background(), Request{Owner: "a", Repo: "b"})
	if err == nil {
		t.Error("expected error for missing question")
	}
}
