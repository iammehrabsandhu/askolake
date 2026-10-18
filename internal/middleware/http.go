// Package middleware holds Gin HTTP middleware and Answerer/LLM decorators.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// Context keys set by handlers for the request log line.
const (
	KeyMode     = "mode"
	KeyCached   = "cached"
	KeyLLMCalls = "llm_calls"
	KeyInTok    = "in_tokens"
	KeyOutTok   = "out_tokens"
	KeyQuestion = "question"
	keyReqID    = "request_id"
)

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type errorBody struct {
	Error string `json:"error"`
}

// RequestID reuses a well-formed X-Request-ID or generates one.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !requestIDRe.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		c.Set(keyReqID, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// Recover turns panics into a logged 500.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in handler", "request_id", c.GetString(keyReqID), "panic", r, "stack", string(debug.Stack()))
				c.AbortWithStatusJSON(http.StatusInternalServerError, errorBody{Error: "internal error"})
			}
		}()
		c.Next()
	}
}

// Logger writes one JSON line per request.
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		q := c.GetString(KeyQuestion)
		if q != "" {
			q, _ = utils.TruncateChars(utils.Redact(q), tuning.LogQuestionMax)
		}
		log.Info("request",
			"request_id", c.GetString(keyReqID),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"mode", c.GetString(KeyMode),
			"cached", c.GetBool(KeyCached),
			"llm_calls", c.GetInt(KeyLLMCalls),
			"in_tokens", c.GetInt(KeyInTok),
			"out_tokens", c.GetInt(KeyOutTok),
			"question", q,
		)
	}
}

// OriginMatcher matches Origin headers against ALLOWED_ORIGINS entries
// (exact, or scheme://host:* for any port).
type OriginMatcher struct {
	exact    map[string]bool
	anyPorts map[string]bool // "scheme://host"
}

// NewOriginMatcher builds a matcher from validated config entries.
func NewOriginMatcher(origins []string) *OriginMatcher {
	m := &OriginMatcher{exact: map[string]bool{}, anyPorts: map[string]bool{}}
	for _, o := range origins {
		o = strings.ToLower(strings.TrimRight(o, "/"))
		if base, ok := strings.CutSuffix(o, ":*"); ok {
			m.anyPorts[base] = true
		} else {
			m.exact[o] = true
		}
	}
	return m
}

// Allowed reports whether origin may call the API.
func (m *OriginMatcher) Allowed(origin string) bool {
	origin = strings.ToLower(origin)
	if origin == "" {
		return false
	}
	if m.exact[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return m.anyPorts[u.Scheme+"://"+u.Hostname()]
}

// CORS sets CORS headers for allowed origins and answers preflights with 204.
func CORS(m *OriginMatcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		c.Writer.Header().Add("Vary", "Origin")
		if m.Allowed(origin) {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
			h.Set("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// BodyLimit caps the request body; handlers see *http.MaxBytesError on overflow.
func BodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > n {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, errorBody{Error: "request too large"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}

// IsTooLarge reports whether err comes from an exceeded BodyLimit.
func IsTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// ClientIP returns the caller IP: CF-Connecting-IP, True-Client-IP, the
// rightmost valid X-Forwarded-For entry, else the remote address.
func ClientIP(c *gin.Context) string {
	for _, h := range []string{"CF-Connecting-IP", "True-Client-IP"} {
		if ip := net.ParseIP(strings.TrimSpace(c.GetHeader(h))); ip != nil {
			return ip.String()
		}
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

type visitor struct {
	lim  *rate.Limiter
	seen time.Time
}

// RateLimiter enforces per-IP and global request rates.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	global   *rate.Limiter
	now      func() time.Time
}

// NewRateLimiter starts a limiter whose idle-entry sweeper stops with ctx.
func NewRateLimiter(ctx context.Context) *RateLimiter {
	rl := &RateLimiter{
		visitors: map[string]*visitor{},
		global:   rate.NewLimiter(rate.Every(time.Minute/tuning.GlobalRatePerMin), tuning.GlobalRateBurst),
		now:      time.Now,
	}
	go func() {
		t := time.NewTicker(tuning.RateSweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rl.sweep()
			}
		}
	}()
	return rl
}

func (rl *RateLimiter) sweep() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := rl.now().Add(-tuning.RateIdleTTL)
	for ip, v := range rl.visitors {
		if v.seen.Before(cutoff) {
			delete(rl.visitors, ip)
		}
	}
}

// Allow reports whether a request from ip may proceed.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	v, ok := rl.visitors[ip]
	if !ok {
		v = &visitor{lim: rate.NewLimiter(rate.Every(time.Minute/tuning.RatePerIPPerMin), tuning.RateBurst)}
		rl.visitors[ip] = v
	}
	v.seen = rl.now()
	rl.mu.Unlock()
	return v.lim.Allow() && rl.global.Allow()
}

// Middleware rejects over-limit requests with 429.
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rl.Allow(ClientIP(c)) {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, errorBody{Error: "Too many questions. Please wait a minute and try again."})
			return
		}
		c.Next()
	}
}
