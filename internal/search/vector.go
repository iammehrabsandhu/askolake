package search

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// Vector is an exact cosine searcher over embedding pieces.
type Vector struct {
	emb     domain.Embedder
	vectors []float32
	dim     int
	owner   []int
	nChunks int
	floor   float64       // relevance floor for this model (0 = tuning.FloorCosine)
	timeout time.Duration // query embedding deadline
}

// NewVector validates the vectors against the embedder and returns a searcher.
func NewVector(emb domain.Embedder, vectors []float32, dim int, owner []int, nChunks int) (*Vector, error) {
	if emb == nil {
		return nil, fmt.Errorf("vector search: nil embedder")
	}
	if d := emb.Info().Dim; d != dim {
		return nil, fmt.Errorf("vector search: embedder dim %d != index dim %d", d, dim)
	}
	if dim <= 0 || len(vectors) != len(owner)*dim {
		return nil, fmt.Errorf("vector search: %d floats for %d pieces of dim %d", len(vectors), len(owner), dim)
	}
	for _, o := range owner {
		if o < 0 || o >= nChunks {
			return nil, fmt.Errorf("vector search: piece owner %d out of range", o)
		}
	}
	return &Vector{emb: emb, vectors: vectors, dim: dim, owner: owner, nChunks: nChunks, timeout: tuning.TimeoutQueryEmbed}, nil
}

// SetFloor sets the relevance floor stamped on every hit (0 = tuning.FloorCosine).
func (v *Vector) SetFloor(f float64) *Vector { v.floor = f; return v }

// SetTimeout sets the query embedding deadline.
func (v *Vector) SetTimeout(d time.Duration) *Vector { v.timeout = d; return v }

// Name implements domain.Searcher.
func (v *Vector) Name() string { return "vector" }

// Search implements domain.Searcher.
func (v *Vector) Search(ctx context.Context, q domain.Query, k int) []domain.Hit {
	if q.Raw == "" || k <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	out, err := v.emb.Embed(ctx, []string{q.Raw})
	if err != nil || len(out) != 1 || len(out[0]) != v.dim {
		slog.Warn("vector search: query embedding failed", "err", err)
		return nil
	}
	qv := out[0]
	best := make([]float64, v.nChunks)
	for i := range best {
		best[i] = math.Inf(-1)
	}
	for row, owner := range v.owner {
		vec := v.vectors[row*v.dim : (row+1)*v.dim]
		var dot float32
		for j, x := range vec {
			dot += x * qv[j]
		}
		if d := float64(dot); d > best[owner] {
			best[owner] = d
		}
	}
	hits := make([]domain.Hit, 0, 64)
	for c, s := range best {
		if !math.IsInf(s, -1) {
			hits = append(hits, domain.Hit{ChunkID: c, Score: s, Cosine: s, CosineFloor: v.floor})
		}
	}
	sortHits(hits)
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
