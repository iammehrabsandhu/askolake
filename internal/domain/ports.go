package domain

import (
	"context"
	"errors"
)

// LLMRequest is a single-turn completion request.
type LLMRequest struct {
	Model, System, User string
	MaxTokens           int
	Temperature         float64
}

// LLMResponse is a completion result with token usage.
type LLMResponse struct {
	Text                string
	InTokens, OutTokens int
}

// LLM completes a prompt.
type LLM interface {
	Complete(ctx context.Context, req LLMRequest) (LLMResponse, error)
}

// Embedder turns texts into vectors.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error) // L2-normalized rows
	Info() EmbedInfo                                                // Provider, Model, Dim
}

// Searcher returns ranked hits for a query.
type Searcher interface {
	Name() string
	Search(ctx context.Context, q Query, k int) []Hit // never errors; returns nil on internal failure (log it)
}

// Answerer answers a question.
type Answerer interface {
	Answer(ctx context.Context, question string) Result
}

// Cache stores results by key.
type Cache interface {
	Get(key string) (Result, bool)
	Add(key string, r Result)
}

// ErrBudget is returned when the daily token budget is exhausted.
var ErrBudget = errors.New("llm budget exhausted")

// ErrLLMDisabled is returned when no LLM is configured.
var ErrLLMDisabled = errors.New("llm disabled")
