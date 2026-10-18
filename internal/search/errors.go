package search

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"olake-ask/internal/domain"
	"olake-ask/internal/utils"
)

type errEntry struct {
	chunk   int
	re      *regexp.Regexp
	literal string // longest static piece, lowercased
}

// Errors matches pasted error messages against known Go error formats.
type Errors struct{ entries []errEntry }

// NewErrors builds the searcher from chunks with an ErrPattern.
func NewErrors(chunks []domain.Chunk) *Errors {
	e := &Errors{}
	bad := 0
	for _, c := range chunks {
		if c.ErrPattern == "" {
			continue
		}
		re, err := regexp.Compile(c.ErrPattern)
		lit := strings.ToLower(utils.Longest(utils.ErrorLiterals(c.ErrPattern)))
		if err != nil || lit == "" {
			bad++
			continue
		}
		e.entries = append(e.entries, errEntry{chunk: c.ID, re: re, literal: lit})
	}
	if bad > 0 {
		slog.Warn("error search: skipped invalid patterns", "count", bad)
	}
	return e
}

// Len returns the number of indexed error patterns.
func (e *Errors) Len() int { return len(e.entries) }

// Name implements domain.Searcher.
func (e *Errors) Name() string { return "errors" }

// Search implements domain.Searcher; only error-like queries are matched.
func (e *Errors) Search(_ context.Context, q domain.Query, k int) []domain.Hit {
	if !q.ErrorLike || k <= 0 {
		return nil
	}
	low := strings.ToLower(q.Raw)
	var matched []errEntry
	for _, en := range e.entries {
		if strings.Contains(low, en.literal) && en.re.MatchString(q.Raw) {
			matched = append(matched, en)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if len(matched[i].literal) != len(matched[j].literal) {
			return len(matched[i].literal) > len(matched[j].literal)
		}
		return matched[i].chunk < matched[j].chunk
	})
	if len(matched) > k {
		matched = matched[:k]
	}
	hits := make([]domain.Hit, len(matched))
	for i, m := range matched {
		hits[i] = domain.Hit{ChunkID: m.chunk, Score: float64(len(m.literal))}
	}
	return hits
}
