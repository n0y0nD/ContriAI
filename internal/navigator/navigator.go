// Package navigator implements ContriAI's Repository Navigator.
// Given a GitHub repository (owner/repo) and a free-form question, it
// downloads the source, searches it for keyword-relevant files and snippets,
// builds a grounded prompt, and returns an LLM-generated answer citing the
// actual files it used.
//
// Strategy (Phase 3 — local retrieval-augmented generation):
//  1. Download the repo as a tarball via GitHub REST API → temp dir.
//  2. Chunk supported source files and build a local TF-IDF vector index.
//  3. Retrieve the most relevant chunks by cosine similarity and send their
//     bounded context to the existing ai.Provider.Complete interface.
//  4. Parse {answer, cited_files} from the response.
//  5. Clean up the temp dir after use.
package navigator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"contribai/internal/ai"
	"contribai/internal/rag"
)

// ──────────────────────────────────────────────────────────────────────────────
// Public types
// ──────────────────────────────────────────────────────────────────────────────

// Request is the input to Navigator.Ask.
type Request struct {
	Owner    string // GitHub repository owner
	Repo     string // GitHub repository name
	Question string // Free-form question about the codebase
	Ref      string // Branch/tag/SHA to fetch. Defaults to "HEAD".
}

// Response is the output of Navigator.Ask.
type Response struct {
	Answer          string           `json:"answer"`
	CitedFiles      []string         `json:"cited_files"`
	RetrievedChunks []RetrievedChunk `json:"retrieved_chunks"`
	Provider        string           `json:"provider"`
}

// RetrievedChunk exposes the exact source range used to ground an answer.
type RetrievedChunk struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Navigator
// ──────────────────────────────────────────────────────────────────────────────

// Navigator orchestrates repo download, code search and LLM querying.
type Navigator struct {
	provider   ai.Provider
	httpClient *http.Client
	token      string // optional GitHub token

	// Tarball base URL — overridable for tests.
	tarballBase string
}

// New creates a Navigator. token may be empty.
func New(provider ai.Provider, token string) *Navigator {
	return &Navigator{
		provider:    provider,
		httpClient:  &http.Client{Timeout: 60 * time.Second},
		token:       token,
		tarballBase: "https://api.github.com",
	}
}

// newWithBase is the test constructor that overrides both the tarball URL and
// optionally the HTTP client used to fetch it.
func newWithBase(provider ai.Provider, token, base string, hc *http.Client) *Navigator {
	n := New(provider, token)
	n.tarballBase = base
	if hc != nil {
		n.httpClient = hc
	}
	return n
}

// ──────────────────────────────────────────────────────────────────────────────
// Simple repo cache — avoids re-downloading within a short window
// ──────────────────────────────────────────────────────────────────────────────

type cacheEntry struct {
	dir     string
	index   *rag.Index
	expires time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cacheEntry{}
)

const cacheTTL = 5 * time.Minute

func cacheKey(owner, repo, ref string) string {
	return owner + "/" + repo + "@" + ref
}

// acquireRepo returns a directory containing the extracted repo, creating it
// if needed. The returned cleanup func must be called when the caller is done
// (if this is the last caller referencing the entry it removes the dir).
//
// For Phase 2 the implementation is simple: each unique (owner, repo, ref)
// lives in one temp dir that expires after cacheTTL. On expiry the old dir is
// deleted and a fresh one is created. This keeps disk usage bounded.
func (n *Navigator) acquireRepo(ctx context.Context, owner, repo, ref string) (dir string, err error) {
	key := cacheKey(owner, repo, ref)

	cacheMu.Lock()
	if e, ok := cache[key]; ok && time.Now().Before(e.expires) {
		cacheMu.Unlock()
		return e.dir, nil
	}
	// Evict expired entry if any.
	if e, ok := cache[key]; ok {
		go os.RemoveAll(e.dir) // best-effort
		delete(cache, key)
	}
	cacheMu.Unlock()

	dir, err = n.downloadRepo(ctx, owner, repo, ref)
	if err != nil {
		return "", err
	}

	cacheMu.Lock()
	cache[key] = cacheEntry{dir: dir, expires: time.Now().Add(cacheTTL)}
	cacheMu.Unlock()

	// Schedule cleanup after TTL. This is fire-and-forget; the server process
	// is expected to be long-lived and bounded in usage.
	go func() {
		time.Sleep(cacheTTL + 10*time.Second)
		cacheMu.Lock()
		e, ok := cache[key]
		if ok && e.dir == dir {
			delete(cache, key)
		}
		cacheMu.Unlock()
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("navigator: cleanup %s: %v", dir, err)
		}
	}()

	return dir, nil
}

// acquireIndex returns the local vector index for a cached repository. The
// index is built once per downloaded snapshot and lives for the same TTL.
func (n *Navigator) acquireIndex(ctx context.Context, owner, repo, ref string) (*rag.Index, error) {
	dir, err := n.acquireRepo(ctx, owner, repo, ref)
	if err != nil {
		return nil, err
	}
	key := cacheKey(owner, repo, ref)
	cacheMu.Lock()
	if entry, ok := cache[key]; ok && entry.dir == dir && entry.index != nil {
		cacheMu.Unlock()
		return entry.index, nil
	}
	cacheMu.Unlock()

	index, err := rag.Build(dir)
	if err != nil {
		return nil, err
	}
	cacheMu.Lock()
	if entry, ok := cache[key]; ok && entry.dir == dir {
		entry.index = index
		cache[key] = entry
	}
	cacheMu.Unlock()
	return index, nil
}

// Retrieve returns source chunks relevant to a repository question or issue.
// It is shared by the repository navigator and issue analyzer so both features
// ground model output in the same cached local RAG index.
func (n *Navigator) Retrieve(ctx context.Context, owner, repo, ref, query string) ([]rag.Chunk, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("retrieval query is required")
	}
	index, err := n.acquireIndex(ctx, owner, repo, ref)
	if err != nil {
		return nil, err
	}
	return index.Search(query, maxMatchFiles), nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Repo download (GitHub tarball)
// ──────────────────────────────────────────────────────────────────────────────

func (n *Navigator) downloadRepo(ctx context.Context, owner, repo, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	url := fmt.Sprintf("%s/repos/%s/%s/tarball/%s", n.tarballBase, owner, repo, ref)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building tarball request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "contribai/0.2 (+phase2 navigator)")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching tarball: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("GitHub tarball API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpDir, err := os.MkdirTemp("", "contribai-repo-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}

	if err := extractTarGz(resp.Body, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("extracting tarball: %w", err)
	}

	// GitHub tarballs wrap everything in a top-level dir like
	// "owner-repo-<sha>/".  Walk into it so callers see files directly.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("reading extracted dir: %w", err)
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(tmpDir, entries[0].Name()), nil
	}
	return tmpDir, nil
}

// extractTarGz reads a .tar.gz stream and writes it into destDir, guarding
// against path traversal (zip-slip).
func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Guard against path traversal.
		target := filepath.Join(destDir, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue // skip suspicious entry
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Cap individual file size at 4 MB to avoid memory pressure.
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, 4<<20))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Code search
// ──────────────────────────────────────────────────────────────────────────────

// searchResult represents a file + a handful of matching lines.
type searchResult struct {
	Path    string
	Score   int
	Matches []string // up to maxMatchLines lines
}

const (
	maxMatchFiles = 8   // how many files to pass to the LLM
	maxMatchLines = 5   // lines per file included in the prompt
	maxSnippetLen = 200 // chars per line before truncation
)

// searchRepo walks repoDir for files whose names or contents match keywords
// extracted from question. Returns the top results sorted by relevance.
func searchRepo(repoDir, question string) []searchResult {
	keywords := extractKeywords(question)
	if len(keywords) == 0 {
		return nil
	}

	var mu sync.Mutex
	var results []searchResult

	// Walk the repo; skip obvious non-source dirs.
	_ = filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		name := d.Name()
		// Skip hidden dirs (except .github), vendor, node_modules, test fixtures, build artefacts.
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && name != ".github" {
				return filepath.SkipDir
			}
			switch name {
			case "vendor", "node_modules", "dist", "build", "__pycache__", "target":
				return filepath.SkipDir
			}
			return nil
		}

		// Only index source-code files.
		if !isSourceFile(name) {
			return nil
		}

		relPath, _ := filepath.Rel(repoDir, path)

		// Score filename.
		score := scoreText(relPath, keywords)

		// Score content (read the file; cap at 64 KB).
		content, readErr := readFileCapped(path, 64<<10)
		var matchLines []string
		if readErr == nil {
			score += scoreText(content, keywords)
			matchLines = findMatchingLines(content, keywords, maxMatchLines)
		}

		if score == 0 && len(matchLines) == 0 {
			return nil
		}

		mu.Lock()
		results = append(results, searchResult{Path: relPath, Score: score, Matches: matchLines})
		mu.Unlock()
		return nil
	})

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Path < results[j].Path
	})
	if len(results) > maxMatchFiles {
		results = results[:maxMatchFiles]
	}
	return results
}

// isSourceFile returns true for common source code extensions (no binaries).
func isSourceFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".java", ".kt", ".c", ".cpp",
		".h", ".hpp", ".rs", ".rb", ".php", ".cs", ".swift", ".scala", ".sh",
		".bash", ".zsh", ".fish", ".lua", ".r", ".jl", ".ex", ".exs", ".erl",
		".hs", ".ml", ".mli", ".clj", ".cljs", ".lisp", ".el",
		".yaml", ".yml", ".toml", ".json", ".tf", ".hcl", ".mod", ".sum",
		".md", ".txt", ".rst", ".adoc":
		return true
	}
	return false
}

// readFileCapped reads at most maxBytes of a file into a string.
func readFileCapped(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes))
	return string(b), err
}

// scoreText counts how many keywords appear in s.
func scoreText(s string, keywords []string) int {
	lower := strings.ToLower(s)
	score := 0
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}
	return score
}

// findMatchingLines returns up to maxLines non-empty lines from content that
// contain at least one keyword.
func findMatchingLines(content string, keywords []string, maxLines int) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if len(out) >= maxLines {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				if len(trimmed) > maxSnippetLen {
					trimmed = trimmed[:maxSnippetLen] + "…"
				}
				out = append(out, trimmed)
				break
			}
		}
	}
	return out
}

// stopWords are common English words that don't help as search keywords.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true,
	"has": true, "had": true, "do": true, "does": true, "did": true,
	"will": true, "would": true, "shall": true, "should": true, "can": true,
	"could": true, "may": true, "might": true, "must": true, "to": true,
	"of": true, "in": true, "on": true, "at": true, "by": true, "for": true,
	"with": true, "about": true, "as": true, "into": true, "through": true,
	"and": true, "or": true, "but": true, "not": true, "what": true,
	"where": true, "which": true, "who": true, "how": true, "when": true,
	"this": true, "that": true, "these": true, "those": true, "it": true,
	"its": true, "from": true, "their": true, "my": true, "your": true,
	"all": true, "any": true, "some": true, "such": true, "no": true,
	"so": true, "if": true, "then": true, "than": true, "also": true,
	"file": true, "files": true, "code": true, "repo": true, "repository": true,
	"function": true, "method": true, "class": true, "module": true,
	"package": true, "implement": true, "implemented": true,
}

// extractKeywords splits the question into lowercase tokens, drops stop words
// and very short tokens, and deduplicates. Returns at most 12 keywords.
func extractKeywords(question string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.FieldsFunc(question, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		kw := strings.ToLower(raw)
		if len(kw) <= 2 || stopWords[kw] || seen[kw] {
			continue
		}
		seen[kw] = true
		out = append(out, kw)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// ──────────────────────────────────────────────────────────────────────────────
// Prompt building
// ──────────────────────────────────────────────────────────────────────────────

func buildAskPrompt(owner, repo, question string, results []rag.Chunk) string {
	var b strings.Builder

	b.WriteString("You are ContriAI, an assistant that answers questions about a GitHub repository's source code.\n")
	b.WriteString("Answer the developer's question using ONLY the code context provided below.\n")
	b.WriteString("At the end of your answer, list the filenames you actually used as evidence under 'CITED_FILES:'.\n")
	b.WriteString("Keep your answer concise and practical — this is a contributor trying to understand the codebase.\n\n")

	fmt.Fprintf(&b, "REPOSITORY: %s/%s\n\n", owner, repo)
	fmt.Fprintf(&b, "QUESTION: %s\n\n", question)

	if len(results) == 0 {
		b.WriteString("CODE CONTEXT: (no keyword matches found in the repository)\n\n")
	} else {
		b.WriteString("CODE CONTEXT (from real source files — use only these):\n\n")
		for _, r := range results {
			fmt.Fprintf(&b, "--- FILE: %s ---\n", r.Path)
			fmt.Fprintf(&b, "--- LINES: %d-%d ---\n", r.StartLine, r.EndLine)
			b.WriteString(r.Content)
			b.WriteString("\n")
			b.WriteString("\n")
		}
	}

	b.WriteString("Respond with ONLY a JSON object in this exact shape (no markdown fences, no other text):\n")
	b.WriteString(`{"answer":"your answer here","cited_files":["path/to/file1","path/to/file2"]}`)
	b.WriteString("\nIn cited_files list ONLY filenames that appear in CODE CONTEXT above.\n")

	return b.String()
}

// ──────────────────────────────────────────────────────────────────────────────
// Response parsing
// ──────────────────────────────────────────────────────────────────────────────

var jsonExtract = regexp.MustCompile(`(?s)\{.*\}`)

type rawAskOutput struct {
	Answer     string   `json:"answer"`
	CitedFiles []string `json:"cited_files"`
}

func parseAskOutput(raw string) (answer string, cited []string) {
	// Try to find a JSON object even if the model wrapped it in prose.
	match := jsonExtract.FindString(raw)
	if match == "" {
		return strings.TrimSpace(raw), nil
	}
	var out rawAskOutput
	if err := json.Unmarshal([]byte(match), &out); err != nil {
		return strings.TrimSpace(raw), nil
	}
	return out.Answer, out.CitedFiles
}

// ──────────────────────────────────────────────────────────────────────────────
// Main entry point
// ──────────────────────────────────────────────────────────────────────────────

// Ask answers a free-form question about a repository by downloading it,
// searching the source code, and prompting the configured ai.Provider.
func (n *Navigator) Ask(ctx context.Context, req Request) (*Response, error) {
	if req.Owner == "" || req.Repo == "" {
		return nil, fmt.Errorf("owner and repo are required")
	}
	if strings.TrimSpace(req.Question) == "" {
		return nil, fmt.Errorf("question is required")
	}

	results, err := n.Retrieve(ctx, req.Owner, req.Repo, req.Ref, req.Question)
	if err != nil {
		return nil, fmt.Errorf("preparing repository index: %w", err)
	}
	prompt := buildAskPrompt(req.Owner, req.Repo, req.Question, results)

	raw, err := n.provider.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI provider (%s): %w", n.provider.Name(), err)
	}

	answer, cited := parseAskOutput(raw)
	retrieved := make([]RetrievedChunk, len(results))
	for i, chunk := range results {
		retrieved[i] = RetrievedChunk{
			Path: chunk.Path, StartLine: chunk.StartLine, EndLine: chunk.EndLine,
			Score: chunk.Score,
		}
	}

	return &Response{
		Answer:          answer,
		CitedFiles:      cited,
		RetrievedChunks: retrieved,
		Provider:        n.provider.Name(),
	}, nil
}
