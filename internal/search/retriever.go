package search

import (
	"context"

	"golang.org/x/sync/errgroup"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// Retriever runs all searchers and fuses their results.
type Retriever struct {
	prep       *Prep
	chunks     []domain.Chunk
	keyword    *Keyword
	vector     domain.Searcher // nil if off
	errs       *Errors
	deprecated []data.Deprecated
}

// Retrieval is the fused outcome of one retrieval pass.
type Retrieval struct {
	Query       domain.Query
	Hits        []domain.Hit // fused, up to Candidates
	MaxCoverage float64
	MaxCosine   float64 // best cosine of the vector list with the most headroom over its floor
	CosineFloor float64 // floor that applies to MaxCosine (0 = tuning.FloorCosine)
	ErrHits     int
}

// NewRetriever wires the searchers. vector may be nil.
func NewRetriever(prep *Prep, chunks []domain.Chunk, keyword *Keyword, vector domain.Searcher, errs *Errors, deprecated []data.Deprecated) *Retriever {
	return &Retriever{prep: prep, chunks: chunks, keyword: keyword, vector: vector, errs: errs, deprecated: deprecated}
}

// Retrieve searches with q plus any extra (rewritten) query strings.
func (r *Retriever) Retrieve(ctx context.Context, q domain.Query, extra []string) Retrieval {
	const (
		kwOrig = iota
		kwExtra
		vec
		errs
	)
	type job struct {
		s    domain.Searcher
		q    domain.Query
		kind int
	}
	jobs := []job{{s: r.keyword, q: q, kind: kwOrig}}
	if r.vector != nil {
		jobs = append(jobs, job{s: r.vector, q: q, kind: vec})
	}
	if r.errs != nil {
		jobs = append(jobs, job{s: r.errs, q: q, kind: errs})
	}
	for _, e := range extra {
		eq := r.prep.Prepare(e)
		jobs = append(jobs, job{s: r.keyword, q: eq, kind: kwExtra})
		if r.vector != nil {
			jobs = append(jobs, job{s: r.vector, q: eq, kind: vec})
		}
	}
	lists := make([][]domain.Hit, len(jobs))
	var g errgroup.Group
	for i, j := range jobs {
		g.Go(func() error {
			lists[i] = j.s.Search(ctx, j.q, tuning.Candidates)
			return nil
		})
	}
	_ = g.Wait() // searchers never return errors

	ret := Retrieval{Query: q}
	// Start from "no vector signal" (cosine 0 at the default floor). With a
	// single model this is exactly max(Cosine), as before per-model floors.
	best := -tuning.FloorCosine
	for i, j := range jobs {
		switch j.kind {
		case kwOrig:
			for _, h := range lists[i] {
				ret.MaxCoverage = max(ret.MaxCoverage, h.Coverage)
			}
		case vec:
			// Lists may come from different models (API or local fallback),
			// each with its own floor: keep the one furthest above its floor.
			for _, h := range lists[i] {
				floor := h.CosineFloor
				if floor == 0 {
					floor = tuning.FloorCosine
				}
				if h.Cosine-floor > best {
					ret.MaxCosine, ret.CosineFloor, best = h.Cosine, floor, h.Cosine-floor
				}
			}
		case errs:
			ret.ErrHits = len(lists[i])
		}
	}
	ret.Hits = Fuse(lists, r.chunks, q, tuning.Candidates, r.deprecated)
	return ret
}

// Prep returns the query preparer.
func (r *Retriever) Prep() *Prep { return r.prep }

// Chunk returns the chunk with the given ID.
func (r *Retriever) Chunk(id int) domain.Chunk { return r.chunks[id] }

// Chunks returns all chunks.
func (r *Retriever) Chunks() []domain.Chunk { return r.chunks }

// VectorsOn reports whether vector search is enabled.
func (r *Retriever) VectorsOn() bool { return r.vector != nil }
