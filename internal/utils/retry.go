package utils

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"olake-ask/internal/tuning"
)

// Backoff returns the delay before retry number attempt (0-based):
// base·2^attempt plus jitter, capped at RetryMax.
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 8 {
		attempt = 8
	}
	d := tuning.RetryBase<<attempt + time.Duration(rand.Int64N(int64(tuning.RetryJitter)+1))
	if d > tuning.RetryMax {
		d = tuning.RetryMax
	}
	return d
}

// RetryAfter parses a Retry-After header (seconds or HTTP date), capped at RetryMax.
func RetryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	} else {
		return 0, false
	}
	if d < 0 {
		d = 0
	}
	if d > tuning.RetryMax {
		d = tuning.RetryMax
	}
	return d, true
}
