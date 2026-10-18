package client

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"olake-ask/internal/tuning"
)

// Web fetches pages and sitemaps for the crawler.
type Web struct {
	c       *http.Client
	headers map[string]string
}

// NewWeb returns a crawler client sending the given User-Agent.
func NewWeb(userAgent string) *Web {
	return &Web{c: NewHTTPClient(), headers: map[string]string{"User-Agent": userAgent, "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}}
}

// Get fetches url with a per-request timeout and one retry.
func (w *Web) Get(ctx context.Context, url string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, tuning.FetchTimeout)
	defer cancel()
	b, _, status, err := GetBytes(ctx, w.c, url, w.headers, tuning.FetchRetries)
	return b, status, err
}

type sitemapDoc struct {
	XMLName  xml.Name
	URLs     []sitemapLoc `xml:"url"`
	Sitemaps []sitemapLoc `xml:"sitemap"`
}

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

// Sitemap returns all <loc> URLs of a sitemap, following one level of <sitemapindex>.
func (w *Web) Sitemap(ctx context.Context, url string) ([]string, error) {
	return w.sitemap(ctx, url, 0)
}

func (w *Web) sitemap(ctx context.Context, url string, depth int) ([]string, error) {
	b, _, err := w.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap %s: %w", url, err)
	}
	var doc sitemapDoc
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse sitemap %s: %w", url, err)
	}
	var out []string
	if doc.XMLName.Local == "sitemapindex" {
		if depth > 0 {
			return nil, fmt.Errorf("sitemap %s: nested sitemap index too deep", url)
		}
		for _, s := range doc.Sitemaps {
			child, err := w.sitemap(ctx, strings.TrimSpace(s.Loc), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, child...)
		}
		return out, nil
	}
	for _, u := range doc.URLs {
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			out = append(out, loc)
		}
	}
	return out, nil
}
