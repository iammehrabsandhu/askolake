package seed

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"olake-ask/internal/client"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// DocsResult reports the docs crawl.
type DocsResult struct {
	Chunks []domain.Chunk
	Pages  int
	Failed []string
}

// Docs crawls the docs sitemap. Any error is fatal for the seeder.
func Docs(ctx context.Context, web *client.Web, cfg data.Docs) (DocsResult, error) {
	var res DocsResult
	locs, err := web.Sitemap(ctx, cfg.Sitemap)
	if err != nil {
		return res, fmt.Errorf("docs: %w", err)
	}
	urls := FilterDocURLs(locs, cfg)
	if len(urls) == 0 {
		return res, fmt.Errorf("docs: sitemap %s has no URLs under %v", cfg.Sitemap, cfg.IncludePrefixes)
	}
	res.Pages = len(urls)

	type page struct {
		chunks []domain.Chunk
		ok     bool
	}
	pages := make([]page, len(urls))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < tuning.CrawlConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				pages[i].chunks, pages[i].ok = crawlPage(ctx, web, urls[i])
				select {
				case <-ctx.Done():
				case <-time.After(tuning.CrawlDelay):
				}
			}
		}()
	}
	for i := range urls {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("docs: %w", err)
	}
	for i, p := range pages {
		if !p.ok {
			res.Failed = append(res.Failed, urls[i])
			continue
		}
		res.Chunks = append(res.Chunks, p.chunks...)
	}
	ratio := float64(len(res.Failed)) / float64(len(urls))
	if ratio > tuning.MaxDocsPageFailRatio || len(res.Chunks) < tuning.MinDocsChunks {
		first := urls[0]
		if len(res.Failed) > 0 {
			first = res.Failed[0]
		}
		return res, fmt.Errorf("docs extraction failed — inspect HTML of %s and update selectors in internal/seed/html.go (failed pages %d/%d, chunks %d, need ≥ %d)",
			first, len(res.Failed), len(urls), len(res.Chunks), tuning.MinDocsChunks)
	}
	return res, nil
}

func crawlPage(ctx context.Context, web *client.Web, u string) ([]domain.Chunk, bool) {
	body, _, err := web.Get(ctx, u)
	if err != nil {
		slog.Warn("docs: fetch failed", "url", u, "err", err)
		return nil, false
	}
	title, sections, err := ExtractDocusaurus(u, body)
	if err != nil {
		slog.Warn("docs: extract failed", "url", u, "err", err)
		return nil, false
	}
	words := 0
	for _, s := range sections {
		for _, b := range s.Blocks {
			words += wc(b.Text)
		}
	}
	if len(sections) == 0 || words < tuning.MinPageWords {
		slog.Warn("docs: page has no content", "url", u, "title", title, "sections", len(sections), "words", words)
		return nil, false
	}
	return Chunk(sections, Meta{PageURL: u, Source: domain.SourceDocs}), true
}

// FilterDocURLs keeps sitemap URLs under the include prefixes and outside the
// excludes, normalized (trailing slash, no query/fragment), deduped and sorted.
func FilterDocURLs(locs []string, cfg data.Docs) []string {
	seen := make(map[string]bool)
	var out []string
	for _, raw := range locs {
		p, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || p.Host == "" {
			continue
		}
		p.RawQuery, p.Fragment = "", ""
		if !strings.HasSuffix(p.Path, "/") {
			p.Path += "/"
		}
		u := p.String()
		if !hasAnyPrefix(u, cfg.IncludePrefixes) || containsAny(u, cfg.ExcludeSubstrings) || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func hasAnyPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
