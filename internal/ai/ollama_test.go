package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaProviderRequestsBoundedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ollamaGenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Format != "json" {
			t.Errorf("expected JSON mode, got %q", request.Format)
		}
		if request.Options.NumPredict != 600 || request.Options.Temperature != 0.2 {
			t.Errorf("unexpected generation options: %#v", request.Options)
		}
		json.NewEncoder(w).Encode(ollamaGenerateResponse{Response: `{"answer":"ok"}`, Done: true})
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
	got, err := provider.Complete(context.Background(), "respond with JSON")
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if got != `{"answer":"ok"}` {
		t.Errorf("unexpected response: %q", got)
	}
}
