package ask

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/search"
	"olake-ask/internal/tuning"
)

// Exhauster reports whether the LLM budget is used up.
type Exhauster interface{ Exhausted() bool }

// Options configures a Service.
type Options struct {
	Retriever   *search.Retriever
	LLM         domain.LLM // budget-wrapped; nil = search-only
	Budget      Exhauster  // may be nil
	Model       string
	Fallback    string
	Rewrite     string // off | when_needed | always
	Rerank      bool
	Data        data.Data
	Identifiers map[string]struct{}
}

// Service is the core answer pipeline (domain.Answerer).
type Service struct {
	r           *search.Retriever
	llm         domain.LLM
	budget      Exhauster
	model       string
	fallback    string
	rewrite     string
	rerank      bool
	data        data.Data
	identifiers map[string]struct{}
}

// NewService returns a Service.
func NewService(o Options) *Service {
	rw := o.Rewrite
	if rw == "" {
		rw = "off"
	}
	return &Service{r: o.Retriever, llm: o.LLM, budget: o.Budget, model: o.Model, fallback: o.Fallback,
		rewrite: rw, rerank: o.Rerank, data: o.Data, identifiers: o.Identifiers}
}

func (s *Service) exhausted() bool { return s.budget != nil && s.budget.Exhausted() }

type tally struct{ calls, in, out int }

func (t *tally) add(r domain.LLMResponse) { t.calls++; t.in += r.InTokens; t.out += r.OutTokens }

func (t tally) apply(r domain.Result) domain.Result {
	r.LLMCalls, r.InTok, r.OutTok = t.calls, t.in, t.out
	if r.Sources == nil {
		r.Sources = []string{}
	}
	return r
}

func (s *Service) call(ctx context.Context, t *tally, req domain.LLMRequest) (domain.LLMResponse, error) {
	resp, err := s.llm.Complete(ctx, req)
	if err == nil {
		t.add(resp)
	} else if !errors.Is(err, domain.ErrBudget) {
		t.calls++ // a failed attempt still reached the provider
	}
	return resp, err
}

// Answer implements domain.Answerer.
func (s *Service) Answer(ctx context.Context, q string) domain.Result {
	var t tally
	comm := s.data.Sources.Community
	query := s.r.Prep().Prepare(q)

	// Optional rewrite.
	var extra []string
	var base *search.Retrieval
	if s.llm != nil && s.rewrite != "off" && !s.exhausted() {
		need := s.rewrite == "always"
		if s.rewrite == "when_needed" {
			b := s.r.Retrieve(ctx, query, nil)
			base = &b
			need = query.NonEnglish || b.MaxCoverage < tuning.WeakCoverage
		}
		if need {
			rctx, cancel := context.WithTimeout(ctx, tuning.TimeoutRewrite)
			resp, err := s.call(rctx, &t, domain.LLMRequest{Model: s.model, System: RewritePrompt, User: q, MaxTokens: tuning.AuxMaxTokens, Temperature: tuning.Temperature})
			cancel()
			if err != nil {
				slog.Warn("rewrite failed; using original query", "err", err)
			} else {
				extra = ParseRewrite(resp.Text)
			}
		}
	}
	var ret search.Retrieval
	if base != nil && len(extra) == 0 {
		ret = *base
	} else {
		ret = s.r.Retrieve(ctx, query, extra)
	}

	// Relevance floor (no LLM call).
	if BelowFloor(ret, s.r.VectorsOn()) {
		return t.apply(DontKnow(comm, nil))
	}
	if s.llm == nil || s.exhausted() {
		return t.apply(SearchOnly(comm, ret.Hits[:min(tuning.SearchOnlyResults, len(ret.Hits))], s.r.Chunk))
	}

	// Selection (+ optional rerank).
	sel := ret.Hits[:min(tuning.TopK, len(ret.Hits))]
	if s.rerank {
		cands := s.chunks(ret.Hits)
		rctx, cancel := context.WithTimeout(ctx, tuning.TimeoutRerank)
		resp, err := s.call(rctx, &t, domain.LLMRequest{Model: s.model, System: RerankPrompt, User: RerankUser(q, cands), MaxTokens: tuning.AuxMaxTokens, Temperature: tuning.Temperature})
		cancel()
		if err != nil {
			slog.Warn("rerank failed; using fused order", "err", err)
		} else if ids, conf, ok := ParseRerank(resp.Text); !ok {
			slog.Warn("rerank output unparsable; using fused order")
		} else if conf == "none" {
			return t.apply(DontKnow(comm, suggestions(ret.Hits, s.r.Chunk, tuning.Suggestions)))
		} else {
			var picked []domain.Hit
			used := map[int]bool{}
			for _, id := range ids {
				if id < 1 || id > len(ret.Hits) || used[id] {
					continue
				}
				used[id] = true
				picked = append(picked, ret.Hits[id-1])
				if len(picked) == tuning.RerankTopK {
					break
				}
			}
			if len(picked) > 0 {
				sel = picked
			}
		}
	}

	// The single answer call (with optional fallback model).
	selChunks := s.chunks(sel)
	req := domain.LLMRequest{Model: s.model, System: AnswerSystem, User: AnswerUser(q, BuildContext(selChunks)),
		MaxTokens: tuning.AnswerMaxTokens, Temperature: tuning.Temperature}
	actx, cancel := context.WithTimeout(ctx, tuning.TimeoutAnswer)
	resp, err := s.call(actx, &t, req)
	cancel()
	if err != nil && s.fallback != "" && !errors.Is(err, domain.ErrBudget) {
		slog.Warn("answer call failed; trying fallback model", "model", s.model, "fallback", s.fallback, "err", err)
		req.Model = s.fallback
		actx, cancel := context.WithTimeout(ctx, tuning.TimeoutAnswer)
		resp, err = s.call(actx, &t, req)
		cancel()
	}
	if err != nil {
		slog.Warn("answer call failed; returning search results", "err", err)
		return t.apply(SearchOnly(comm, sel[:min(tuning.SearchOnlyResults, len(sel))], s.r.Chunk))
	}

	answerable, body := ParseAnswer(resp.Text, len(selChunks))
	v := Verify(answerable, body, selChunks, q, s.identifiers, s.data)
	if !v.OK {
		return t.apply(DontKnow(comm, suggestions(sel, s.r.Chunk, tuning.Suggestions)))
	}
	return t.apply(domain.Result{Answer: strings.TrimSpace(v.Answer), Sources: v.Sources, Mode: domain.ModeAnswered})
}

// BelowFloor reports whether retrieval is too weak to attempt an answer.
func BelowFloor(ret search.Retrieval, vectorsOn bool) bool {
	if len(ret.Hits) == 0 {
		return true
	}
	floor := ret.CosineFloor
	if floor == 0 {
		floor = tuning.FloorCosine
	}
	return ret.ErrHits == 0 && ret.MaxCoverage < tuning.FloorCoverage &&
		(!vectorsOn || ret.MaxCosine < floor)
}

func (s *Service) chunks(hits []domain.Hit) []domain.Chunk {
	out := make([]domain.Chunk, len(hits))
	for i, h := range hits {
		out[i] = s.r.Chunk(h.ChunkID)
	}
	return out
}
