package search

import (
	"context"
	"math"
	"strings"

	"golang.org/x/sync/errgroup"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// Retriever runs all searchers and fuses their results.
type Retriever struct {
	prep       *Prep
	chunks     []domain.Chunk
	keyword    *Keyword
	vector     domain.Searcher // nil if off
	errs       *Errors
	deprecated []data.Deprecated
	overview   overview
}

// overview pins product overview sections for product-only questions.
type overview struct {
	terms map[string]bool          // tokens of the product names
	pages map[domain.Product][]int // chunk IDs in listed order
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

// SetOverview configures the overview sections for product-only questions
// (sources.yaml overview). Without it, no sections are pinned.
func (r *Retriever) SetOverview(o data.Overview) {
	r.overview = overview{terms: map[string]bool{}, pages: map[domain.Product][]int{}}
	for _, t := range o.Terms {
		for _, tok := range utils.Tokenize(t) {
			r.overview.terms[tok] = true
		}
	}
	for prod, subs := range o.Pages {
		for _, sub := range subs {
			for _, c := range r.chunks {
				if strings.Contains(c.URL, sub) {
					r.overview.pages[prod] = append(r.overview.pages[prod], c.ID)
				}
			}
		}
	}
}

// overviewFor returns the overview chunk IDs when every content term of q
// is a product-name token ("What is OLake?"), else nil.
func (r *Retriever) overviewFor(q domain.Query) []int {
	if len(q.Content) == 0 || len(r.overview.terms) == 0 {
		return nil
	}
	prod := domain.ProductOLake
	for _, t := range q.Content {
		if !r.overview.terms[t] {
			return nil
		}
		if t == "fusion" {
			prod = domain.ProductFusion
		}
	}
	return r.overview.pages[prod]
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
	if pins := r.overviewFor(q); len(pins) > 0 {
		ret.Hits = pinFirst(ret.Hits, pins, tuning.Candidates)
	}
	return ret
}

// pinFirst puts the pinned chunks first (in order), then the other hits.
func pinFirst(hits []domain.Hit, pins []int, limit int) []domain.Hit {
	out := make([]domain.Hit, 0, limit)
	seen := map[int]bool{}
	for _, id := range pins {
		if !seen[id] && len(out) < limit {
			seen[id] = true
			out = append(out, domain.Hit{ChunkID: id, Score: math.Inf(1)})
		}
	}
	for _, h := range hits {
		if !seen[h.ChunkID] && len(out) < limit {
			seen[h.ChunkID] = true
			out = append(out, h)
		}
	}
	return out
}

// Prep returns the query preparer.
func (r *Retriever) Prep() *Prep { return r.prep }

// Chunk returns the chunk with the given ID.
func (r *Retriever) Chunk(id int) domain.Chunk { return r.chunks[id] }

// Chunks returns all chunks.
func (r *Retriever) Chunks() []domain.Chunk { return r.chunks }

// VectorsOn reports whether vector search is enabled.
func (r *Retriever) VectorsOn() bool { return r.vector != nil }
