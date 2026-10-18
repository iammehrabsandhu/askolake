// Package search implements keyword, vector and error-message retrieval and
// rank fusion over the in-memory index.
package search

import (
	"regexp"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

var errorLikeRe = regexp.MustCompile(`(?i)\b(error|failed|failure|panic|exception|unable to|cannot|could not|denied|timeout|refused)\b`)

// Prep turns raw questions into domain.Query values.
type Prep struct {
	synonyms     [][][]string // group → phrase → tokens
	productTerms map[domain.Product][][]string
}

// NewPrep builds a Prep from the data files.
func NewPrep(d data.Data) *Prep {
	p := &Prep{productTerms: make(map[domain.Product][][]string)}
	for _, g := range d.Synonyms {
		var group [][]string
		for _, phrase := range g {
			if toks := utils.Tokenize(phrase); len(toks) > 0 {
				group = append(group, toks)
			}
		}
		if len(group) > 1 {
			p.synonyms = append(p.synonyms, group)
		}
	}
	for prod, terms := range d.Products.QueryTerms {
		for _, t := range terms {
			if toks := utils.Tokenize(t); len(toks) > 0 {
				p.productTerms[prod] = append(p.productTerms[prod], toks)
			}
		}
	}
	return p
}

// Prepare tokenizes raw, expands synonyms and detects product and flags.
func (p *Prep) Prepare(raw string) domain.Query {
	toks := utils.Tokenize(raw)
	q := domain.Query{
		Raw:        raw,
		Content:    utils.ContentTerms(raw),
		ErrorLike:  len(raw) >= 20 && errorLikeRe.MatchString(raw),
		NonEnglish: utils.NonASCIIRatio(raw) > tuning.NonASCIIRatio,
		Mentions:   strings.ToLower(raw),
	}
	have := make(map[string]struct{}, len(q.Content))
	for _, t := range q.Content {
		have[t] = struct{}{}
		q.Terms = append(q.Terms, domain.Term{Text: t, Weight: 1})
	}
	limit := 2 * tuning.MaxQueryTerms
	for _, group := range p.synonyms {
		matched := -1
		for i, phrase := range group {
			if containsSeq(toks, phrase) {
				matched = i
				break
			}
		}
		if matched < 0 {
			continue
		}
		for i, phrase := range group {
			if i == matched {
				continue
			}
			for _, t := range phrase {
				if _, ok := have[t]; ok || len(q.Terms) >= limit {
					continue
				}
				have[t] = struct{}{}
				q.Terms = append(q.Terms, domain.Term{Text: t, Weight: tuning.SynonymWeight})
			}
		}
	}
	var found []domain.Product
	for prod, phrases := range p.productTerms {
		for _, ph := range phrases {
			if containsSeq(toks, ph) {
				found = append(found, prod)
				break
			}
		}
	}
	if len(found) == 1 {
		q.Product = found[0]
	}
	return q
}

// containsSeq reports whether seq appears contiguously in toks.
func containsSeq(toks, seq []string) bool {
	if len(seq) == 0 || len(seq) > len(toks) {
		return false
	}
outer:
	for i := 0; i+len(seq) <= len(toks); i++ {
		for j := range seq {
			if toks[i+j] != seq[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
