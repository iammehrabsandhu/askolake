package client

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// EmbeddingAPI is an OpenAI-compatible /embeddings client (domain.Embedder).
type EmbeddingAPI struct {
	c       *http.Client
	url     string
	model   string
	headers map[string]string
	mu      sync.Mutex
	dim     int
}

// NewEmbeddingAPI returns a client. dim may be 0; it is learned from the first response.
func NewEmbeddingAPI(baseURL, apiKey, model string, dim int) *EmbeddingAPI {
	return &EmbeddingAPI{
		c:       NewHTTPClient(),
		url:     strings.TrimRight(baseURL, "/") + "/embeddings",
		model:   model,
		headers: map[string]string{"Authorization": "Bearer " + apiKey},
		dim:     dim,
	}
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed implements domain.Embedder.
func (e *EmbeddingAPI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += tuning.EmbedAPIBatch {
		end := min(start+tuning.EmbedAPIBatch, len(texts))
		var resp embedResp
		if _, err := DoJSON(ctx, e.c, http.MethodPost, e.url, e.headers, embedReq{Model: e.model, Input: texts[start:end]}, &resp, tuning.LLMRetries); err != nil {
			return nil, fmt.Errorf("embeddings: %w", err)
		}
		if len(resp.Data) != end-start {
			return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(resp.Data), end-start)
		}
		sort.Slice(resp.Data, func(i, j int) bool { return resp.Data[i].Index < resp.Data[j].Index })
		for _, d := range resp.Data {
			if len(d.Embedding) == 0 {
				return nil, fmt.Errorf("embeddings: empty vector")
			}
			e.mu.Lock()
			if e.dim == 0 {
				e.dim = len(d.Embedding)
			}
			dim := e.dim
			e.mu.Unlock()
			if len(d.Embedding) != dim {
				return nil, fmt.Errorf("embeddings: vector dim %d, want %d", len(d.Embedding), dim)
			}
			out = append(out, normalize(d.Embedding))
		}
	}
	return out, nil
}

// Info implements domain.Embedder.
func (e *EmbeddingAPI) Info() domain.EmbedInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return domain.EmbedInfo{Provider: "api", Model: e.model, Dim: e.dim}
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
	return v
}
