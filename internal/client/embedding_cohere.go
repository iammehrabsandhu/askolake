package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// Cohere input types: documents are embedded at seed time, questions at query time.
const (
	CohereSearchDocument = "search_document"
	CohereSearchQuery    = "search_query"
)

// CohereEmbed is a Cohere v2 /embed client (domain.Embedder).
type CohereEmbed struct {
	c         *http.Client
	url       string
	model     string
	inputType string
	outDim    int // output_dimension sent to the API; 0 = model default (not sent)
	headers   map[string]string
	mu        sync.Mutex
	dim       int

	// Pacing below tuning.CohereInputsPerMin.
	paceMu    sync.Mutex
	winStart  time.Time
	winInputs int
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// NewCohereEmbed returns a client. dim is the expected vector dimension
// (0 = learned from the first response); outDim is sent as output_dimension
// when > 0.
func NewCohereEmbed(baseURL, apiKey, model, inputType string, outDim, dim int) *CohereEmbed {
	return &CohereEmbed{
		c:         NewHTTPClient(),
		url:       strings.TrimRight(baseURL, "/") + "/embed",
		model:     model,
		inputType: inputType,
		outDim:    outDim,
		headers:   map[string]string{"Authorization": "Bearer " + apiKey},
		dim:       dim,
		now:       time.Now,
		sleep:     sleepCtx,
	}
}

type cohereReq struct {
	Model           string   `json:"model"`
	Texts           []string `json:"texts"`
	InputType       string   `json:"input_type"`
	EmbeddingTypes  []string `json:"embedding_types"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	Truncate        string   `json:"truncate"`
}

type cohereResp struct {
	Embeddings struct {
		Float [][]float32 `json:"float"`
	} `json:"embeddings"`
}

// Embed implements domain.Embedder.
func (e *CohereEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += tuning.CohereEmbedBatch {
		end := min(start+tuning.CohereEmbedBatch, len(texts))
		if err := e.pace(ctx, end-start); err != nil {
			return nil, fmt.Errorf("cohere embed: %w", err)
		}
		req := cohereReq{Model: e.model, Texts: texts[start:end], InputType: e.inputType,
			EmbeddingTypes: []string{"float"}, OutputDimension: e.outDim, Truncate: "END"}
		var resp cohereResp
		if _, err := DoJSON(ctx, e.c, http.MethodPost, e.url, e.headers, req, &resp, tuning.LLMRetries); err != nil {
			return nil, fmt.Errorf("cohere embed: %w", err)
		}
		if len(resp.Embeddings.Float) != end-start {
			return nil, fmt.Errorf("cohere embed: got %d vectors for %d inputs", len(resp.Embeddings.Float), end-start)
		}
		for _, v := range resp.Embeddings.Float {
			if len(v) == 0 {
				return nil, fmt.Errorf("cohere embed: empty vector")
			}
			e.mu.Lock()
			if e.dim == 0 {
				e.dim = len(v)
			}
			dim := e.dim
			e.mu.Unlock()
			if len(v) != dim {
				return nil, fmt.Errorf("cohere embed: vector dim %d, want %d", len(v), dim)
			}
			out = append(out, normalize(v))
		}
	}
	return out, nil
}

// pace blocks until n more inputs fit in the current one-minute window.
func (e *CohereEmbed) pace(ctx context.Context, n int) error {
	e.paceMu.Lock()
	defer e.paceMu.Unlock()
	now := e.now()
	if now.Sub(e.winStart) >= time.Minute {
		e.winStart, e.winInputs = now, 0
	}
	if e.winInputs > 0 && e.winInputs+n > tuning.CohereInputsPerMin {
		if err := e.sleep(ctx, e.winStart.Add(time.Minute).Sub(now)); err != nil {
			return err
		}
		e.winStart, e.winInputs = e.now(), 0
	}
	e.winInputs += n
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Info implements domain.Embedder.
func (e *CohereEmbed) Info() domain.EmbedInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return domain.EmbedInfo{Provider: "cohere", Model: e.model, Dim: e.dim, OutputDim: e.outDim}
}
