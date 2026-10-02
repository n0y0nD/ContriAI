// Package rag builds a small, local retrieval index for source repositories.
// It deliberately uses TF-IDF vectors rather than a hosted embedding service:
// the index is deterministic, inspectable, and keeps repository contents local.
package rag

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	chunkLines   = 60
	overlapLines = 12
	maxFileBytes = 256 << 10
	maxChunks    = 1200
)

// Chunk is a cited section of a repository file.
type Chunk struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
	Score     float64
	terms     map[string]float64
}

// Index is an in-memory vector index over repository source chunks.
type Index struct {
	chunks []Chunk
	df     map[string]int
	total  int
}

// Build indexes source and documentation files below repoDir. Binary files and
// common dependency/build directories are deliberately skipped.
func Build(repoDir string) (*Index, error) {
	index := &Index{df: map[string]int{}}
	err := filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(index.chunks) >= maxChunks {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isIndexable(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileBytes {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil || isBinary(content) {
			return nil
		}
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return nil
		}
		index.addFile(filepath.ToSlash(rel), string(content))
		return nil
	})
	if err != nil {
		return nil, err
	}
	index.total = len(index.chunks)
	for _, chunk := range index.chunks {
		for term := range chunk.terms {
			index.df[term]++
		}
	}
	return index, nil
}

func (i *Index) addFile(path, content string) {
	lines := splitLines(content)
	if len(lines) == 0 {
		return
	}
	step := chunkLines - overlapLines
	for start := 0; start < len(lines) && len(i.chunks) < maxChunks; start += step {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}
		text := strings.Join(lines[start:end], "\n")
		i.chunks = append(i.chunks, Chunk{
			Path: path, StartLine: start + 1, EndLine: end,
			Content: text, terms: termFrequency(path + " " + text),
		})
		if end == len(lines) {
			break
		}
	}
}

// Search returns the best matching chunks, ranked by TF-IDF cosine similarity.
func (i *Index) Search(question string, limit int) []Chunk {
	if i.total == 0 || limit <= 0 {
		return nil
	}
	query := termFrequency(question)
	if len(query) == 0 {
		return nil
	}
	qWeights := i.weights(query)
	qNorm := norm(qWeights)
	if qNorm == 0 {
		return nil
	}
	results := make([]Chunk, 0, len(i.chunks))
	for _, original := range i.chunks {
		weights := i.weights(original.terms)
		denominator := qNorm * norm(weights)
		if denominator == 0 {
			continue
		}
		dot := 0.0
		for term, qWeight := range qWeights {
			dot += qWeight * weights[term]
		}
		if dot == 0 {
			continue
		}
		chunk := original
		chunk.Score = dot / denominator
		results = append(results, chunk)
	}
	sort.Slice(results, func(a, b int) bool {
		if results[a].Score != results[b].Score {
			return results[a].Score > results[b].Score
		}
		if results[a].Path != results[b].Path {
			return results[a].Path < results[b].Path
		}
		return results[a].StartLine < results[b].StartLine
	})
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

func (i *Index) weights(tf map[string]float64) map[string]float64 {
	weights := make(map[string]float64, len(tf))
	for term, count := range tf {
		// Terms absent from the corpus carry no retrieval signal.
		if i.df[term] == 0 {
			continue
		}
		weights[term] = (1 + math.Log(count)) * math.Log(float64(i.total+1)/float64(i.df[term]+1))
	}
	return weights
}

func norm(weights map[string]float64) float64 {
	sum := 0.0
	for _, weight := range weights {
		sum += weight * weight
	}
	return math.Sqrt(sum)
}

func termFrequency(text string) map[string]float64 {
	terms := map[string]float64{}
	for _, term := range tokens(text) {
		terms[term]++
	}
	return terms
}

func tokens(text string) []string {
	var out []string
	for _, raw := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		term := strings.ToLower(raw)
		if len(term) > 2 && !stopWords[term] {
			out = append(out, term)
		}
	}
	return out
}

func splitLines(content string) []string {
	s := bufio.NewScanner(strings.NewReader(content))
	buffer := make([]byte, 64<<10)
	s.Buffer(buffer, maxFileBytes)
	var lines []string
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines
}

func isBinary(content []byte) bool {
	for _, b := range content {
		if b == 0 {
			return true
		}
	}
	return false
}

func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") && name != ".github" {
		return true
	}
	switch name {
	case "vendor", "node_modules", "dist", "build", "__pycache__", "target":
		return true
	}
	return false
}

func isIndexable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".java", ".kt", ".c", ".cpp", ".h", ".hpp", ".rs", ".rb", ".php", ".cs", ".swift", ".sh", ".yaml", ".yml", ".toml", ".json", ".tf", ".md", ".txt", ".rst":
		return true
	}
	return false
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "from": true, "that": true, "this": true, "where": true, "which": true, "what": true, "how": true, "are": true, "was": true, "into": true, "about": true, "file": true, "files": true, "code": true, "repo": true, "repository": true, "implemented": true}
