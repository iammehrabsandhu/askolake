package middleware

import (
	"context"
	"fmt"
	"sync"
	"time"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// Budget is a domain.LLM decorator enforcing a daily token budget (UTC days)
// and a concurrency limit.
type Budget struct {
	inner domain.LLM
	daily int
	sem   chan struct{}
	now   func() time.Time

	mu   sync.Mutex
	day  string
	used int
}

// NewBudget wraps inner; daily == 0 means unlimited.
func NewBudget(inner domain.LLM, daily int) *Budget {
	return NewBudgetWithClock(inner, daily, time.Now)
}

// NewBudgetWithClock is NewBudget with an injectable clock (tests).
func NewBudgetWithClock(inner domain.LLM, daily int, now func() time.Time) *Budget {
	return &Budget{inner: inner, daily: daily, sem: make(chan struct{}, tuning.MaxConcurrentLLM), now: now}
}

// rollover resets usage when the UTC date changes. Caller holds mu.
func (b *Budget) rollover() {
	if d := b.now().UTC().Format(time.DateOnly); d != b.day {
		b.day, b.used = d, 0
	}
}

// Exhausted reports whether today's budget is used up.
func (b *Budget) Exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	return b.daily > 0 && b.used >= b.daily
}

// Used returns tokens used today.
func (b *Budget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	return b.used
}

// Complete implements domain.LLM.
func (b *Budget) Complete(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	if b.Exhausted() {
		return domain.LLMResponse{}, domain.ErrBudget
	}
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return domain.LLMResponse{}, fmt.Errorf("llm concurrency wait: %w", ctx.Err())
	}
	defer func() { <-b.sem }()
	resp, err := b.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	tokens := resp.InTokens + resp.OutTokens
	if tokens == 0 {
		tokens = (len(req.System)+len(req.User))/4 + len(resp.Text)/4
	}
	b.mu.Lock()
	b.rollover()
	b.used += tokens
	b.mu.Unlock()
	return resp, nil
}
