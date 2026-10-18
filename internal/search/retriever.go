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
	// MentionsProduct: the question names a product (sources.yaml overview
	// terms). Such questions always reach the LLM (see ask.BelowFloor).
	MentionsProduct bool
	// Extra is how many passages beyond tuning.TopK the LLM should get
	// (1 when the product overview was added after the top TopK).
	Extra int
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

// overviewFor reports whether q names a product, the overview chunk IDs
// for that product, and whether q is product-only (every content term is a
// product name, "go" counting as part of "OLake Go").
func (r *Retriever) overviewFor(q domain.Query) (mentions bool, pages []int, only bool) {
	if len(q.Content) == 0 || len(r.overview.terms) == 0 {
		return false, nil, false
	}
	prod, only := domain.ProductOLake, true
	for _, t := range q.Content {
		switch {
		case r.overview.terms[t]:
			mentions = true
			if t == "fusion" {
				prod = domain.ProductFusion
			}
		case t != "go":
			only = false
		}
	}
	if !mentions {
		return false, nil, false
	}
	return true, r.overview.pages[prod], only
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
	mentions, pages, only := r.overviewFor(q)
	ret.MentionsProduct = mentions
	switch {
	case only && len(pages) > 0:
		ret.Hits = pinFirst(ret.Hits, pages, tuning.Candidates)
	case mentions && len(pages) > 0:
		ret.Hits, ret.Extra = ensureInTop(ret.Hits, pages[0], tuning.TopK)
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

// ensureInTop makes sure chunk id is among the passages the LLM sees. If it
// is not in the first k hits, it is added right after them (position k+1)
// and extra = 1, so the caller sends k+1 passages and none is pushed out.
func ensureInTop(hits []domain.Hit, id, k int) (out []domain.Hit, extra int) {
	for i, h := range hits {
		if h.ChunkID == id {
			if i < k {
				return hits, 0
			}
			hits = append(hits[:i:i], hits[i+1:]...)
			break
		}
	}
	pos := min(k, len(hits))
	out = make([]domain.Hit, 0, len(hits)+1)
	out = append(out, hits[:pos]...)
	out = append(out, domain.Hit{ChunkID: id})
	return append(out, hits[pos:]...), 1
}

// Prep returns the query preparer.
func (r *Retriever) Prep() *Prep { return r.prep }

// Chunk returns the chunk with the given ID.
func (r *Retriever) Chunk(id int) domain.Chunk { return r.chunks[id] }

// Chunks returns all chunks.
func (r *Retriever) Chunks() []domain.Chunk { return r.chunks }

// VectorsOn reports whether vector search is enabled.
func (r *Retriever) VectorsOn() bool { return r.vector != nil }
