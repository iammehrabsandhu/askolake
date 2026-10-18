package search

import (
	"context"
	"math"
	"slices"
	"sort"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

type posting struct {
	chunk   int32
	tfBody  uint16
	tfTitle uint16
}

// Keyword is an in-memory BM25F-lite index (title + body fields).
type Keyword struct {
	termID   map[string]int32
	postings [][]posting
	termSets [][]int32 // per chunk, sorted term ids
	dl       []float64
	avgdl    float64
	n        int
}

// NewKeyword indexes chunks. Chunk IDs must equal their slice positions.
func NewKeyword(chunks []domain.Chunk) *Keyword {
	k := &Keyword{termID: make(map[string]int32), n: len(chunks), termSets: make([][]int32, len(chunks)), dl: make([]float64, len(chunks))}
	var total float64
	for i, c := range chunks {
		title, body := utils.Tokenize(c.Title), utils.Tokenize(c.Text)
		tf := make(map[int32]*posting)
		add := func(toks []string, isTitle bool) {
			for _, t := range toks {
				id, ok := k.termID[t]
				if !ok {
					id = int32(len(k.postings))
					k.termID[t] = id
					k.postings = append(k.postings, nil)
				}
				p := tf[id]
				if p == nil {
					p = &posting{chunk: int32(i)}
					tf[id] = p
				}
				if isTitle {
					p.tfTitle = sat(p.tfTitle)
				} else {
					p.tfBody = sat(p.tfBody)
				}
			}
		}
		add(title, true)
		add(body, false)
		set := make([]int32, 0, len(tf))
		for id, p := range tf {
			k.postings[id] = append(k.postings[id], *p)
			set = append(set, id)
		}
		slices.Sort(set)
		k.termSets[i] = set
		k.dl[i] = float64(len(title) + len(body))
		total += k.dl[i]
	}
	if k.n > 0 {
		k.avgdl = total / float64(k.n)
	}
	if k.avgdl == 0 {
		k.avgdl = 1
	}
	return k
}

func sat(v uint16) uint16 {
	if v == math.MaxUint16 {
		return v
	}
	return v + 1
}

// Name implements domain.Searcher.
func (k *Keyword) Name() string { return "keyword" }

// Search implements domain.Searcher.
func (k *Keyword) Search(_ context.Context, q domain.Query, limit int) []domain.Hit {
	if len(q.Terms) == 0 || k.n == 0 || limit <= 0 {
		return nil
	}
	scores := make(map[int32]float64)
	for _, term := range q.Terms {
		id, ok := k.termID[term.Text]
		if !ok {
			continue
		}
		plist := k.postings[id]
		df := float64(len(plist))
		idf := math.Log(1 + (float64(k.n)-df+0.5)/(df+0.5))
		for _, p := range plist {
			tf := float64(p.tfBody) + tuning.TitleWeight*float64(p.tfTitle)
			norm := tuning.BM25K1 * (1 - tuning.BM25B + tuning.BM25B*k.dl[p.chunk]/k.avgdl)
			scores[p.chunk] += term.Weight * idf * tf * (tuning.BM25K1 + 1) / (tf + norm)
		}
	}
	hits := make([]domain.Hit, 0, len(scores))
	for c, s := range scores {
		if s > 0 {
			hits = append(hits, domain.Hit{ChunkID: int(c), Score: s})
		}
	}
	sortHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for i := range hits {
		hits[i].Coverage = k.Coverage(hits[i].ChunkID, q.Content)
	}
	return hits
}

// Coverage returns the fraction of content terms present in the chunk.
func (k *Keyword) Coverage(chunkID int, content []string) float64 {
	if len(content) == 0 || chunkID < 0 || chunkID >= k.n {
		return 0
	}
	set := k.termSets[chunkID]
	found := 0
	for _, t := range content {
		if id, ok := k.termID[t]; ok {
			if _, ok := slices.BinarySearch(set, id); ok {
				found++
			}
		}
	}
	return float64(found) / float64(len(content))
}

// sortHits orders by score desc, then chunk ID asc.
func sortHits(h []domain.Hit) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Score != h[j].Score {
			return h[i].Score > h[j].Score
		}
		return h[i].ChunkID < h[j].ChunkID
	})
}
