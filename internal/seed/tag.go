package seed

import (
	"crypto/sha256"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// Tag sets product (docs only), deprecated and low-value flags in place.
func Tag(chunks []domain.Chunk, d data.Data) {
	for i := range chunks {
		c := &chunks[i]
		if c.Source == domain.SourceDocs {
			c.Product = productForURL(c.URL, d.Products)
		}
		c.Deprecated = isDeprecated(*c, d.Deprecated)
		c.LowValue = containsAny(c.URL, d.Sources.LowValue)
	}
}

func productForURL(url string, p data.Products) domain.Product {
	for _, r := range p.URLRules {
		if strings.Contains(url, r.Contains) {
			return r.Product
		}
	}
	return p.Default
}

func isDeprecated(c domain.Chunk, dep []data.Deprecated) bool {
	if strings.Contains(strings.ToLower(c.Title), "deprecat") {
		return true
	}
	for _, d := range dep {
		for _, p := range d.Pages {
			if strings.Contains(c.URL, p) {
				return true
			}
		}
	}
	return false
}

// Dedupe drops chunks whose normalized text repeats on the same page (e.g.
// tab content rendered twice), keeping the one with the higher source weight
// (first seen on ties). Order is preserved. Copies on different pages are
// kept so every page stays complete; search removes them at query time.
func Dedupe(chunks []domain.Chunk) []domain.Chunk {
	key := func(c domain.Chunk) [32]byte { return sha256.Sum256([]byte(c.Page + "\x00" + utils.Normalize(c.Text))) }
	best := make(map[[32]byte]int) // key → index into chunks of the winner
	for i, c := range chunks {
		k := key(c)
		j, ok := best[k]
		if !ok || tuning.SourceWeight[c.Source] > tuning.SourceWeight[chunks[j].Source] {
			best[k] = i
		}
	}
	out := make([]domain.Chunk, 0, len(best))
	for i, c := range chunks {
		if best[key(c)] == i {
			out = append(out, c)
		}
	}
	return out
}
