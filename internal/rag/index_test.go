package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearchRetrievesRelevantChunkAndLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth", "token.go"), []byte("package auth\n\n// ValidateToken validates bearer tokens.\nfunc ValidateToken(token string) bool { return token != \"\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.go"), []byte("package server\nfunc Start() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	index, err := Build(dir)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	results := index.Search("Where is token validation?", 3)
	if len(results) == 0 {
		t.Fatal("expected a matching chunk")
	}
	if results[0].Path != "auth/token.go" {
		t.Errorf("expected auth/token.go first, got %q", results[0].Path)
	}
	if results[0].StartLine != 1 || results[0].EndLine != 4 {
		t.Errorf("unexpected source range: %d-%d", results[0].StartLine, results[0].EndLine)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive similarity score, got %f", results[0].Score)
	}
}

func TestBuildSkipsDependenciesAndBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "vendor", "ignored.go"), []byte("package ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, "binary.go"), []byte{'a', 0, 'b'}, 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	index, err := Build(dir)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(index.chunks) != 1 || index.chunks[0].Path != "main.go" {
		t.Errorf("expected only main.go to be indexed, got %#v", index.chunks)
	}
}
