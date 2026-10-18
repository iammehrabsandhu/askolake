// Command eval measures retrieval quality and end-to-end answer behaviour.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"olake-ask/internal/app"
	"olake-ask/internal/ask"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/index"
	"olake-ask/internal/middleware"
	"olake-ask/internal/search"
	"olake-ask/internal/tuning"
)

type golden struct {
	Question    string   `json:"question"`
	Expect      []string `json:"expect_url_contains"`
	MustContain []string `json:"must_contain"` // facts a correct answer needs; checked in the LLM context (retrieval) or answer (full)
}

type refuseCase struct {
	Question string `json:"question"`
}

func main() {
	mode := flag.String("mode", "retrieval", "retrieval | full")
	remote := flag.String("url", "", "full mode: call POST <url>/ask instead of the in-process answerer")
	k := flag.Int("k", tuning.TopK, "recall cut-off")
	goldenPath := flag.String("golden", "eval/golden.jsonl", "golden questions")
	refusePath := flag.String("refuse", "eval/must_refuse.jsonl", "must-refuse questions")
	reportPath := flag.String("report", "eval/report.md", "report output")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	report, err := run(*mode, *remote, *k, *goldenPath, *refusePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval setup failed:", err)
		os.Exit(1)
	}
	fmt.Print(report)
	if err := os.WriteFile(*reportPath, []byte(report), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
}

func run(mode, remote string, k int, goldenPath, refusePath string) (string, error) {
	gold, err := readJSONL[golden](goldenPath)
	if err != nil {
		return "", err
	}
	refuse, err := readJSONL[refuseCase](refusePath)
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadServer()
	if err != nil {
		return "", err
	}
	d, err := data.Load()
	if err != nil {
		return "", err
	}
	b, err := app.LoadBundle()
	if err != nil {
		return "", err
	}
	warns := validateExpectations(gold, b)
	switch mode {
	case "retrieval":
		r := app.NewRetriever(context.Background(), cfg, d, b)
		return retrievalReport(r, b, gold, refuse, k, warns), nil
	case "full":
		var answer func(q string) (domain.Result, time.Duration, error)
		if remote != "" {
			answer = remoteAnswerer(strings.TrimRight(remote, "/"))
		} else {
			a, _, err := app.NewAnswerer(cfg, d, b)
			if err != nil {
				return "", err
			}
			answer = func(q string) (domain.Result, time.Duration, error) {
				start := time.Now()
				r := a.Answer(context.Background(), q)
				return r, time.Since(start), nil
			}
		}
		return fullReport(answer, b, gold, refuse, cfg, remote, warns), nil
	}
	return "", fmt.Errorf("unknown -mode %q (retrieval | full)", mode)
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func validateExpectations(gold []golden, b index.Bundle) []string {
	var warns []string
	for _, g := range gold {
		for _, e := range g.Expect {
			found := false
			for _, c := range b.Chunks {
				if strings.Contains(c.URL, e) {
					found = true
					break
				}
			}
			if !found {
				warns = append(warns, fmt.Sprintf("expected substring %q (question %q) matches no chunk URL", e, g.Question))
			}
		}
		for _, f := range g.MustContain {
			found := false
			for _, c := range b.Chunks {
				if strings.Contains(fold(c.Title+"\n"+c.Text), fold(f)) {
					found = true
					break
				}
			}
			if !found {
				warns = append(warns, fmt.Sprintf("fact %q (question %q) is in no chunk — unreachable", f, g.Question))
			}
		}
	}
	return warns
}

// fold lowercases and collapses whitespace for fact matching.
func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// missingFacts returns the facts not contained in text.
func missingFacts(text string, facts []string) []string {
	t := fold(text)
	var miss []string
	for _, f := range facts {
		if !strings.Contains(t, fold(f)) {
			miss = append(miss, f)
		}
	}
	return miss
}

func matches(url string, expect []string) bool {
	for _, e := range expect {
		if strings.Contains(url, e) {
			return true
		}
	}
	return false
}

// guardBlocked reports whether the input guards alone would refuse q.
func guardBlocked(q string) bool {
	r := middleware.WithGuards(passAnswerer{}).Answer(context.Background(), q)
	return r.Mode == domain.ModeRefused
}

type passAnswerer struct{}

func (passAnswerer) Answer(context.Context, string) domain.Result {
	return domain.Result{Mode: domain.ModeAnswered}
}

func retrievalReport(r *search.Retriever, b index.Bundle, gold []golden, refuse []refuseCase, k int, warns []string) string {
	ctx := context.Background()
	var hits int
	var rr float64
	var floored int
	var misses []string
	var factQs, factQsFull, factsTotal, factsFound, ctxChars int
	var factMisses []string
	for _, g := range gold {
		ret := r.Retrieve(ctx, r.Prep().Prepare(g.Question), nil)
		if ask.BelowFloor(ret, r.VectorsOn()) {
			floored++
		}
		// The context the LLM would receive by default: the top-k fused chunks.
		var sb strings.Builder
		for i, h := range ret.Hits {
			if i >= k {
				break
			}
			c := r.Chunk(h.ChunkID)
			sb.WriteString(c.Title + "\n" + c.Text + "\n\n")
		}
		ctxChars += sb.Len()
		if len(g.MustContain) > 0 {
			miss := missingFacts(sb.String(), g.MustContain)
			factQs++
			factsTotal += len(g.MustContain)
			factsFound += len(g.MustContain) - len(miss)
			if len(miss) == 0 {
				factQsFull++
			} else {
				factMisses = append(factMisses, fmt.Sprintf("| %s | %s | %d/%d |", g.Question, strings.Join(miss, ", "), len(g.MustContain)-len(miss), len(g.MustContain)))
			}
		}
		rank := 0
		for i, h := range ret.Hits {
			if i >= k {
				break
			}
			if matches(r.Chunk(h.ChunkID).URL, g.Expect) {
				rank = i + 1
				break
			}
		}
		if rank > 0 {
			hits++
			rr += 1 / float64(rank)
		} else {
			top := ""
			if len(ret.Hits) > 0 {
				top = r.Chunk(ret.Hits[0].ChunkID).URL
			}
			misses = append(misses, fmt.Sprintf("| %s | %s | %s |", g.Question, strings.Join(g.Expect, ", "), top))
		}
	}
	var blocked int
	var leaks []string
	for _, c := range refuse {
		ret := r.Retrieve(ctx, r.Prep().Prepare(c.Question), nil)
		if guardBlocked(c.Question) || ask.BelowFloor(ret, r.VectorsOn()) {
			blocked++
		} else {
			leaks = append(leaks, fmt.Sprintf("| %s | %.2f | %.3f |", c.Question, ret.MaxCoverage, ret.MaxCosine))
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Eval report — retrieval mode\n\n")
	fmt.Fprintf(&sb, "Index `%s` · %d chunks · vectors %v · %s\n\n", b.Manifest.Version, len(b.Chunks), r.VectorsOn(), time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&sb, "| Golden questions | %d |\n", len(gold))
	fmt.Fprintf(&sb, "| Recall@%d | %.3f |\n", k, ratio(hits, len(gold)))
	fmt.Fprintf(&sb, "| MRR@%d | %.3f |\n", k, rr/float64(max(1, len(gold))))
	fmt.Fprintf(&sb, "| Golden below floor (false dont_know) | %d |\n", floored)
	fmt.Fprintf(&sb, "| **Questions with all answer facts in top-%d context** | **%.3f** (%d/%d) |\n", k, ratio(factQsFull, factQs), factQsFull, factQs)
	fmt.Fprintf(&sb, "| Answer facts in top-%d context (per fact) | %.3f (%d/%d) |\n", k, ratio(factsFound, factsTotal), factsFound, factsTotal)
	fmt.Fprintf(&sb, "| Avg top-%d context size | %d chars (~%d tokens) |\n", k, ctxChars/max(1, len(gold)), ctxChars/max(1, len(gold))/4)
	fmt.Fprintf(&sb, "| Must-refuse questions | %d |\n", len(refuse))
	fmt.Fprintf(&sb, "| Must-refuse blocked (guard or floor) | %.3f |\n", ratio(blocked, len(refuse)))
	fmt.Fprintf(&sb, "\nFloor: coverage < %.2f and cosine < the serving model's floor (default %.2f). Must-refuse questions that pass the floor are left to the LLM (ANSWERABLE: no) in full mode.\n", tuning.FloorCoverage, tuning.FloorCosine)
	if len(misses) > 0 {
		fmt.Fprintf(&sb, "\n## Golden misses\n\n| Question | Expected | Top hit |\n|---|---|---|\n%s\n", strings.Join(misses, "\n"))
	}
	if len(factMisses) > 0 {
		fmt.Fprintf(&sb, "\n## Answer facts missing from the LLM context\n\n| Question | Missing facts | Found |\n|---|---|---|\n%s\n", strings.Join(factMisses, "\n"))
	}
	if len(leaks) > 0 {
		fmt.Fprintf(&sb, "\n## Must-refuse passing the floor\n\n| Question | Max coverage | Max cosine |\n|---|---|---|\n%s\n", strings.Join(leaks, "\n"))
	}
	writeWarnings(&sb, warns)
	return sb.String()
}

func fullReport(answer func(string) (domain.Result, time.Duration, error), b index.Bundle, gold []golden, refuse []refuseCase, cfg config.Server, remote string, warns []string) string {
	var lat []time.Duration
	var answered, sourceHit, dontKnow, errs, tokens, calls, factQs, factQsFull int
	var notes []string
	for _, g := range gold {
		r, d, err := answer(g.Question)
		if err != nil {
			errs++
			notes = append(notes, fmt.Sprintf("| %s | error: %v |", g.Question, err))
			continue
		}
		lat = append(lat, d)
		tokens += r.InTok + r.OutTok
		calls += r.LLMCalls
		switch r.Mode {
		case domain.ModeAnswered:
			answered++
		case domain.ModeDontKnow:
			dontKnow++
		}
		if len(g.MustContain) > 0 {
			factQs++
			if len(missingFacts(r.Answer, g.MustContain)) == 0 {
				factQsFull++
			}
		}
		hit := false
		for _, s := range r.Sources {
			if matches(s, g.Expect) {
				hit = true
				break
			}
		}
		if hit {
			sourceHit++
		} else {
			notes = append(notes, fmt.Sprintf("| %s | %s, no expected source |", g.Question, r.Mode))
		}
	}
	var refused int
	var leaks []string
	for _, c := range refuse {
		r, d, err := answer(c.Question)
		if err != nil {
			errs++
			continue
		}
		lat = append(lat, d)
		tokens += r.InTok + r.OutTok
		calls += r.LLMCalls
		if r.Mode == domain.ModeRefused || r.Mode == domain.ModeDontKnow {
			refused++
		} else {
			leaks = append(leaks, fmt.Sprintf("| %s | %s |", c.Question, r.Mode))
		}
	}
	slices.Sort(lat)
	target := "in-process"
	if remote != "" {
		target = remote
	}
	llm := "off (search_only)"
	if cfg.LLMEnabled() {
		llm = cfg.LLMProvider + "/" + cfg.LLMModel
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Eval report — full mode\n\n")
	fmt.Fprintf(&sb, "Target %s · index `%s` · LLM %s · rewrite %s · rerank %v · %s\n\n", target, b.Manifest.Version, llm, cfg.Rewrite, cfg.Rerank, time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&sb, "| Golden questions | %d |\n", len(gold))
	fmt.Fprintf(&sb, "| Answered rate | %.3f |\n", ratio(answered, len(gold)))
	fmt.Fprintf(&sb, "| Source-hit rate | %.3f |\n", ratio(sourceHit, len(gold)))
	fmt.Fprintf(&sb, "| dont_know rate (golden) | %.3f |\n", ratio(dontKnow, len(gold)))
	fmt.Fprintf(&sb, "| Answers containing all answer facts | %.3f (%d/%d) |\n", ratio(factQsFull, factQs), factQsFull, factQs)
	fmt.Fprintf(&sb, "| Must-refuse refused or dont_know | %.3f |\n", ratio(refused, len(refuse)))
	fmt.Fprintf(&sb, "| Latency p50 | %s |\n", pct(lat, 0.50))
	fmt.Fprintf(&sb, "| Latency p95 | %s |\n", pct(lat, 0.95))
	if remote == "" {
		fmt.Fprintf(&sb, "| LLM calls (total) | %d |\n", calls)
		fmt.Fprintf(&sb, "| Tokens (total) | %d |\n", tokens)
	}
	fmt.Fprintf(&sb, "| Errors | %d |\n", errs)
	if len(notes) > 0 {
		fmt.Fprintf(&sb, "\n## Golden without expected source\n\n| Question | Outcome |\n|---|---|\n%s\n", strings.Join(notes, "\n"))
	}
	if len(leaks) > 0 {
		fmt.Fprintf(&sb, "\n## Must-refuse answered\n\n| Question | Mode |\n|---|---|\n%s\n", strings.Join(leaks, "\n"))
	}
	writeWarnings(&sb, warns)
	return sb.String()
}

func writeWarnings(sb *strings.Builder, warns []string) {
	if len(warns) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n## Warnings\n\n")
	for _, w := range warns {
		fmt.Fprintf(sb, "- %s\n", w)
	}
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func pct(d []time.Duration, p float64) string {
	if len(d) == 0 {
		return "n/a"
	}
	i := int(float64(len(d)-1) * p)
	return d[i].Round(time.Millisecond).String()
}

// remoteAnswerer calls a deployed API, waiting out 429s (Retry-After).
func remoteAnswerer(base string) func(string) (domain.Result, time.Duration, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	return func(q string) (domain.Result, time.Duration, error) {
		body, _ := json.Marshal(map[string]string{"question": q})
		for attempt := 0; attempt < 5; attempt++ {
			start := time.Now()
			resp, err := c.Post(base+"/ask", "application/json", bytes.NewReader(body))
			if err != nil {
				return domain.Result{}, 0, err
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				time.Sleep(61 * time.Second)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				return domain.Result{}, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
			}
			var out struct {
				Answer  string   `json:"answer"`
				Sources []string `json:"sources"`
			}
			if err := json.Unmarshal(b, &out); err != nil {
				return domain.Result{}, 0, err
			}
			return domain.Result{Answer: out.Answer, Sources: out.Sources, Mode: domain.Mode(resp.Header.Get("X-Ask-Mode"))}, time.Since(start), nil
		}
		return domain.Result{}, 0, errors.New("still rate limited after retries")
	}
}
