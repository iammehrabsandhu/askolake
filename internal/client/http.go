// Package client holds every outbound network call (one file per external API).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

const userAgent = "olake-ask/1.0"

// HTTPError is a non-2xx response.
type HTTPError struct {
	Status int
	Body   string // first 512 bytes
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Body)
}

// NewHTTPClient returns a client without a global timeout (deadlines come from ctx).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// do runs a request with the shared retry policy and returns the body of the
// final response. Non-2xx final responses yield *HTTPError.
func do(ctx context.Context, c *http.Client, method, url string, headers map[string]string, body []byte, retries int, limit int64) ([]byte, http.Header, int, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("build request %s: %w", url, err)
		}
		req.Header.Set("User-Agent", userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		var wait time.Duration
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, 0, fmt.Errorf("%s %s: %w", method, url, ctx.Err())
			}
			lastErr = fmt.Errorf("%s %s: %w", method, url, err)
			wait = utils.Backoff(attempt)
			if attempt >= retries {
				return nil, nil, 0, lastErr
			}
		} else {
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, limit))
			resp.Body.Close()
			status := resp.StatusCode
			if status >= 200 && status < 300 {
				if rerr != nil {
					return nil, resp.Header, status, fmt.Errorf("read %s: %w", url, rerr)
				}
				return b, resp.Header, status, nil
			}
			if len(b) > tuning.HTTPErrBody {
				b = b[:tuning.HTTPErrBody]
			}
			lastErr = &HTTPError{Status: status, Body: string(b)}
			if !retryable(status) || attempt >= retries {
				return nil, resp.Header, status, lastErr
			}
			if d, ok := utils.RetryAfter(resp.Header); ok {
				wait = d
			} else {
				wait = utils.Backoff(attempt)
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, nil, 0, errors.Join(lastErr, ctx.Err())
		case <-t.C:
		}
	}
}

// printRequest writes method, URL and the exact JSON body (indented) to w.
// Headers are never printed, so API keys stay out of the terminal.
func printRequest(w io.Writer, method, url string, in any) {
	if w == nil {
		return
	}
	raw, err := json.Marshal(in) // same encoding DoJSON sends
	if err != nil {
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return
	}
	rule := strings.Repeat("─", 72)
	// One write per request so concurrent requests don't interleave.
	fmt.Fprintf(w, "\n%s\nLLM REQUEST  %s %s  (%s)\n%s\n%s\n%s\n",
		rule, method, url, time.Now().Format(time.TimeOnly), rule, pretty.String(), rule)
}

// DoJSON sends in as JSON (when non-nil) and decodes a 2xx response into out.
func DoJSON(ctx context.Context, c *http.Client, method, url string, headers map[string]string, in, out any, retries int) (int, error) {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
		body = b
	}
	b, _, status, err := do(ctx, c, method, url, headers, body, retries, tuning.MaxFetchBytes)
	if err != nil {
		return status, err
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return status, fmt.Errorf("decode response from %s: %w", url, err)
		}
	}
	return status, nil
}

// GetBytes fetches url with the shared retry policy (body limit 10 MB).
func GetBytes(ctx context.Context, c *http.Client, url string, headers map[string]string, retries int) ([]byte, http.Header, int, error) {
	return do(ctx, c, http.MethodGet, url, headers, nil, retries, tuning.MaxFetchBytes)
}
