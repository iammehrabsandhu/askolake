package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

type lruCache struct {
	lru *expirable.LRU[string, domain.Result]
}

func (c lruCache) Get(k string) (domain.Result, bool) { return c.lru.Get(k) }
func (c lruCache) Add(k string, r domain.Result)      { c.lru.Add(k, r) }

// NewLRUCache returns the default size/TTL-bounded cache.
func NewLRUCache() domain.Cache {
	return lruCache{lru: expirable.NewLRU[string, domain.Result](tuning.CacheSize, nil, tuning.CacheTTL)}
}

type cached struct {
	next  domain.Answerer
	cache domain.Cache
	group singleflight.Group
}

// WithCache caches answered results and coalesces concurrent identical questions.
func WithCache(next domain.Answerer) domain.Answerer {
	return WithCacheStore(next, NewLRUCache())
}

// WithCacheStore is WithCache with an explicit store.
func WithCacheStore(next domain.Answerer, c domain.Cache) domain.Answerer {
	return &cached{next: next, cache: c}
}

func cacheKey(q string) string {
	sum := sha256.Sum256([]byte(utils.Normalize(q)))
	return hex.EncodeToString(sum[:])
}

func (c *cached) Answer(ctx context.Context, q string) domain.Result {
	key := cacheKey(q)
	if r, ok := c.cache.Get(key); ok {
		r.Cached, r.LLMCalls, r.InTok, r.OutTok = true, 0, 0, 0
		r.Sources = append([]string{}, r.Sources...)
		return r
	}
	leader := false
	v, _, _ := c.group.Do(key, func() (any, error) {
		leader = true // the closure only runs in the leader's call
		r := c.next.Answer(ctx, q)
		if r.Mode == domain.ModeAnswered {
			c.cache.Add(key, r)
		}
		return r, nil
	})
	r := v.(domain.Result)
	r.Sources = append([]string{}, r.Sources...)
	if !leader {
		// Followers got the leader's result; only the leader is charged the LLM usage.
		r.Cached, r.LLMCalls, r.InTok, r.OutTok = true, 0, 0, 0
	}
	return r
}
