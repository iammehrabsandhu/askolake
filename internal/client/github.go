package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"olake-ask/internal/tuning"
)

const githubAPI = "https://api.github.com"

// GitHub is a minimal REST client for closed issues and their comments.
type GitHub struct {
	c         *http.Client
	base      string
	headers   map[string]string
	mu        sync.Mutex
	remaining int // -1 until the first response
}

// Issue is a GitHub issue (pull requests are filtered out).
type Issue struct {
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	HTMLURL     string           `json:"html_url"`
	Comments    int              `json:"comments"`
	PullRequest *json.RawMessage `json:"pull_request"`
}

// Comment is an issue comment.
type Comment struct {
	Body              string `json:"body"`
	AuthorAssociation string `json:"author_association"`
}

// NewGitHub returns a client; token may be empty (unauthenticated).
func NewGitHub(token string) *GitHub {
	h := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return &GitHub{c: NewHTTPClient(), base: githubAPI, headers: h, remaining: -1}
}

// WithBase overrides the API base URL (tests).
func (g *GitHub) WithBase(base string) *GitHub { g.base = base; return g }

// Remaining returns the last seen X-RateLimit-Remaining (-1 when unknown).
func (g *GitHub) Remaining() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.remaining
}

func (g *GitHub) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, tuning.FetchTimeout)
	defer cancel()
	b, h, _, err := GetBytes(ctx, g.c, g.base+path, g.headers, tuning.FetchRetries)
	if h != nil {
		if v, perr := strconv.Atoi(h.Get("X-RateLimit-Remaining")); perr == nil {
			g.mu.Lock()
			g.remaining = v
			g.mu.Unlock()
		}
	}
	if err != nil {
		return fmt.Errorf("github %s: %w", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("github %s: decode: %w", path, err)
	}
	return nil
}

func (g *GitHub) lowOnQuota() bool {
	r := g.Remaining()
	return r >= 0 && r < tuning.MinRateRemaining
}

// ListClosedIssues lists up to max closed issues (most recently updated first).
func (g *GitHub) ListClosedIssues(ctx context.Context, repo string, max int) ([]Issue, error) {
	var out []Issue
	for page := 1; len(out) < max && page <= tuning.GitHubMaxPages; page++ {
		if g.lowOnQuota() {
			break
		}
		var items []Issue
		path := fmt.Sprintf("/repos/%s/issues?state=closed&per_page=100&page=%d&sort=updated&direction=desc", repo, page)
		if err := g.get(ctx, path, &items); err != nil {
			var he *HTTPError
			if errors.As(err, &he) && he.Status == http.StatusUnprocessableEntity && len(out) > 0 {
				break // GitHub refuses deep page-number pagination; keep what we have
			}
			return out, err
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			if it.PullRequest != nil {
				continue
			}
			out = append(out, it)
			if len(out) >= max {
				break
			}
		}
	}
	return out, nil
}

// ListComments lists the comments of one issue (first 100).
func (g *GitHub) ListComments(ctx context.Context, repo string, number int) ([]Comment, error) {
	var out []Comment
	err := g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, number), &out)
	return out, err
}
