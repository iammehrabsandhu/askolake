package search

import (
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// Fuse merges ranked lists with reciprocal rank fusion, applies source
// weights, the product boost and the deprecated and low-value penalties,
// then drops repeated text, caps hits per page and returns at most limit hits.
func Fuse(lists [][]domain.Hit, chunks []domain.Chunk, q domain.Query, limit int, deprecated []data.Deprecated) []domain.Hit {
	acc := make(map[int]*domain.Hit)
	for _, list := range lists {
		for rank, h := range list {
			if h.ChunkID < 0 || h.ChunkID >= len(chunks) {
				continue
			}
			a := acc[h.ChunkID]
			if a == nil {
				a = &domain.Hit{ChunkID: h.ChunkID}
				acc[h.ChunkID] = a
			}
			a.Score += 1 / (tuning.RRFK + float64(rank+1))
			a.Coverage = max(a.Coverage, h.Coverage)
			a.Cosine = max(a.Cosine, h.Cosine)
		}
	}
	mentionsDeprecat := strings.Contains(q.Mentions, "deprecat")
	out := make([]domain.Hit, 0, len(acc))
	for _, a := range acc {
		c := chunks[a.ChunkID]
		if w, ok := tuning.SourceWeight[c.Source]; ok {
			a.Score *= w
		}
		if q.Product != domain.ProductNone && c.Product == q.Product {
			a.Score *= tuning.ProductBoost
		}
		if c.Deprecated && !mentionsDeprecat && !mentionsDeprecatedTerm(q.Mentions, c.URL, deprecated) {
			a.Score *= tuning.DeprecatedPenalty
		}
		if c.LowValue {
			a.Score *= tuning.LowValuePenalty
		}
		out = append(out, *a)
	}
	sortHits(out)
	perPage := make(map[string]int)
	var kept []map[string]struct{} // token sets of the hits kept so far
	capped := out[:0]
	for _, h := range out {
		page := chunks[h.ChunkID].Page
		if perPage[page] >= tuning.PerPageCap {
			continue
		}
		// The same or nearly the same text on several pages (sibling setup
		// guides, per-driver READMEs) is shown once: the best-ranked copy.
		set := tokenSet(chunks[h.ChunkID].Text)
		if nearDuplicate(set, kept) {
			continue
		}
		kept = append(kept, set)
		perPage[page]++
		capped = append(capped, h)
		if len(capped) == limit {
			break
		}
	}
	return capped
}

func tokenSet(text string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, t := range utils.Tokenize(text) {
		set[t] = struct{}{}
	}
	return set
}

// nearDuplicate reports whether set overlaps any kept set by at least
// tuning.NearDupJaccard (|A∩B| / |A∪B|). Empty sets never match.
func nearDuplicate(set map[string]struct{}, kept []map[string]struct{}) bool {
	for _, k := range kept {
		if len(set) == 0 || len(k) == 0 {
			continue
		}
		inter := 0
		for t := range set {
			if _, ok := k[t]; ok {
				inter++
			}
		}
		if float64(inter) >= tuning.NearDupJaccard*float64(len(set)+len(k)-inter) {
			return true
		}
	}
	return false
}

// mentionsDeprecatedTerm reports whether the question names a deprecated
// term whose pages match url.
func mentionsDeprecatedTerm(mentions, url string, deprecated []data.Deprecated) bool {
	for _, d := range deprecated {
		if !strings.Contains(mentions, strings.ToLower(d.Term)) {
			continue
		}
		for _, p := range d.Pages {
			if strings.Contains(url, p) {
				return true
			}
		}
	}
	return false
}
