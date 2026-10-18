// Package embed wraps go-potion static Model2Vec embeddings as a domain.Embedder.
package embed

import (
	"context"
	"fmt"
	"slices"
	"strings"

	potion "github.com/trengrj/go-potion"

	"olake-ask/internal/domain"
)

// Model2Vec is a local, in-process embedder.
type Model2Vec struct {
	p     *potion.Potion
	model string
}

// New loads (downloading on first use) the named POTION model.
func New(ctx context.Context, model string) (*Model2Vec, error) {
	if !slices.Contains(potion.Models(), potion.Model(model)) {
		names := make([]string, 0, len(potion.Models()))
		for _, m := range potion.Models() {
			names = append(names, string(m))
		}
		return nil, fmt.Errorf("model2vec: unknown model %q (known: %s)", model, strings.Join(names, ", "))
	}
	p, err := potion.New(ctx, potion.Model(model))
	if err != nil {
		return nil, fmt.Errorf("model2vec: load %s: %w", model, err)
	}
	return &Model2Vec{p: p, model: model}, nil
}

// Embed implements domain.Embedder (vectors are already L2-normalized).
func (m *Model2Vec) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := m.p.EncodeMany(texts)
	if err != nil {
		return nil, fmt.Errorf("model2vec: encode: %w", err)
	}
	return out, nil
}

// Info implements domain.Embedder.
func (m *Model2Vec) Info() domain.EmbedInfo {
	return domain.EmbedInfo{Provider: "model2vec", Model: m.model, Dim: m.p.Dimensions()}
}

// Close releases the model weights.
func (m *Model2Vec) Close() error { return m.p.Close() }
