package search

import (
	"context"
	"log/slog"

	"olake-ask/internal/domain"
)

// Fallback searches with primary and, when it returns nothing (vector search
// only does so on failure, e.g. an embedding API error or timeout), with
// secondary.
type Fallback struct {
	primary, secondary domain.Searcher
}

// NewFallback returns a searcher that prefers primary.
func NewFallback(primary, secondary domain.Searcher) *Fallback {
	return &Fallback{primary: primary, secondary: secondary}
}

// Name implements domain.Searcher.
func (f *Fallback) Name() string { return f.primary.Name() }

// Search implements domain.Searcher.
func (f *Fallback) Search(ctx context.Context, q domain.Query, k int) []domain.Hit {
	if hits := f.primary.Search(ctx, q, k); len(hits) > 0 {
		return hits
	}
	if q.Raw == "" || k <= 0 {
		return nil
	}
	slog.Warn("vector search: primary returned nothing; using fallback")
	return f.secondary.Search(ctx, q, k)
}
