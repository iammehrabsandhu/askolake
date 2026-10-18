package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/index"
)

// toggleBundle has local vectors (chunk 0 matches the query) and API vectors
// that are reversed (chunk 1 matches), so the top hit shows which set served.
func toggleBundle(withAPI bool) index.Bundle {
	b := index.Bundle{
		Manifest: domain.Manifest{Embedder: &domain.EmbedInfo{Provider: "api", Model: "local", Dim: 2, Pieces: 2}},
		Chunks: []domain.Chunk{
			{ID: 0, URL: "https://olake.io/a", Page: "https://olake.io/a", Title: "Alpha", Text: "alpha text"},
			{ID: 1, URL: "https://olake.io/b", Page: "https://olake.io/b", Title: "Beta", Text: "beta text"},
		},
		Vectors: []float32{1, 0, 0, 1},
		Pieces:  []int{0, 1},
	}
	if withAPI {
		b.Manifest.APIEmbedder = &domain.EmbedInfo{Provider: "cohere", Model: "embed-v4.0", Dim: 2, Pieces: 2}
		b.APIVectors = []float32{0, 1, 1, 0}
	}
	return b
}

func TestRetrieverEmbedAPIToggle(t *testing.T) {
	d, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Local query embedder (OpenAI-compatible fake) and Cohere fake both return [1,0].
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}}})
	}))
	defer local.Close()
	var cohereCalls atomic.Int32
	cohereDown := atomic.Bool{}
	cohere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cohereCalls.Add(1)
		if cohereDown.Load() {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": [][]float32{{1, 0}}}})
	}))
	defer cohere.Close()

	top := func(cfg config.Server, b index.Bundle) int {
		t.Helper()
		r := NewRetriever(context.Background(), cfg, d, b)
		if !r.VectorsOn() {
			t.Fatal("vectors off")
		}
		ret := r.Retrieve(context.Background(), r.Prep().Prepare("zzz qqq"), nil)
		if len(ret.Hits) == 0 {
			t.Fatal("no hits")
		}
		return ret.Hits[0].ChunkID
	}
	base := config.Server{EmbedBaseURL: local.URL, EmbedAPIKey: "k", EmbedAPI: "auto", EmbedAPIBaseURL: cohere.URL, VectorBackend: "memory"}

	// Bundle without API vectors: local only, Cohere never called.
	if got := top(base, toggleBundle(false)); got != 0 || cohereCalls.Load() != 0 {
		t.Fatalf("no API set: top=%d cohere calls=%d", got, cohereCalls.Load())
	}
	// EMBED_API=off: local only.
	off := base
	off.EmbedAPI = "off"
	if got := top(off, toggleBundle(true)); got != 0 || cohereCalls.Load() != 0 {
		t.Fatalf("EMBED_API=off: top=%d cohere calls=%d", got, cohereCalls.Load())
	}
	// API set + key: Cohere serves.
	if got := top(base, toggleBundle(true)); got != 1 || cohereCalls.Load() == 0 {
		t.Fatalf("API on: top=%d cohere calls=%d", got, cohereCalls.Load())
	}
	// Cohere failing: the same question falls back to the local vectors.
	cohereDown.Store(true)
	if got := top(base, toggleBundle(true)); got != 0 {
		t.Fatalf("API down: top=%d, want local fallback", got)
	}
	// Models reported for the startup log.
	if api, loc := VectorModels(base, toggleBundle(true)); api != "cohere/embed-v4.0" || loc != "api/local" {
		t.Fatalf("models = %q %q", api, loc)
	}
	if api, _ := VectorModels(off, toggleBundle(true)); api != "" {
		t.Fatalf("EMBED_API=off must not report an API model, got %q", api)
	}
}
