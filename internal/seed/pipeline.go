package seed

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"time"

	"olake-ask/internal/client"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/index"
	"olake-ask/internal/tuning"
)

// Options configures a Pipeline. NewEmbedder is nil when EMBEDDER=none.
type Options struct {
	Data          data.Data
	Web           *client.Web
	GitHub        *client.GitHub
	Authenticated bool // GITHUB_TOKEN set
	SeedIssues    bool
	NewEmbedder   func(ctx context.Context) (domain.Embedder, error)
	// NewAPIEmbedder builds optional second vectors (Cohere) over the same
	// pieces; nil = off. Any failure drops only these vectors.
	NewAPIEmbedder func(ctx context.Context) (domain.Embedder, error)
	OutDir         string
	WorkDir        string
	Now            func() time.Time
}

// Pipeline builds and writes the index bundle.
type Pipeline struct{ opt Options }

// Summary describes a finished seed run.
type Summary struct {
	Counts   map[domain.SourceKind]int
	Chunks   int
	Pieces   int
	Version  string
	Embedder *domain.EmbedInfo
	// APIEmbedder describes the optional second vectors; nil when absent.
	APIEmbedder *domain.EmbedInfo
	Repos       map[string]string
	Duration    time.Duration
	OutDir      string
	Warnings    []string
}

// NewPipeline returns a pipeline.
func NewPipeline(opt Options) *Pipeline {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Pipeline{opt: opt}
}

// Run executes the full seed. On error the previous bundle is untouched.
func (p *Pipeline) Run(ctx context.Context) (Summary, error) {
	start := p.opt.Now()
	sum := Summary{OutDir: p.opt.OutDir, Repos: map[string]string{}, Counts: map[domain.SourceKind]int{}}
	warn := func(msg string, args ...any) {
		slog.Warn(msg, args...)
		for i := 0; i+1 < len(args); i += 2 {
			msg += fmt.Sprintf(" %v=%v", args[i], args[i+1])
		}
		sum.Warnings = append(sum.Warnings, msg)
	}
	d := p.opt.Data

	// 1. Repos.
	var checkouts []Checkout
	for _, r := range d.Sources.Repos {
		t := time.Now()
		dir, sha, err := FetchRepo(ctx, p.opt.WorkDir, r.Name, r.URL, r.Ref)
		if err != nil {
			if ctx.Err() != nil {
				return sum, ctx.Err()
			}
			warn("repo fetch failed; skipping its sources", "repo", r.Name, "err", err)
			continue
		}
		sum.Repos[r.Name] = sha
		checkouts = append(checkouts, Checkout{Repo: r, Dir: dir, SHA: sha})
		slog.Info("repo fetched", "repo", r.Name, "sha", sha[:min(12, len(sha))], "ms", time.Since(t).Milliseconds())
	}

	// 2. Sources.
	var all []domain.Chunk
	idents := map[string]struct{}{}

	t := time.Now()
	docs, err := Docs(ctx, p.opt.Web, d.Sources.Docs)
	if err != nil {
		return sum, err
	}
	slog.Info("source docs", "pages", docs.Pages, "failed", len(docs.Failed), "chunks", len(docs.Chunks), "ms", time.Since(t).Milliseconds())
	for _, u := range docs.Failed {
		sum.Warnings = append(sum.Warnings, "docs page failed: "+u)
	}
	all = append(all, docs.Chunks...)

	t = time.Now()
	n := 0
	for _, co := range checkouts {
		chunks, ids, err := RepoDocs(co)
		if err != nil {
			warn("repo source failed", "repo", co.Repo.Name, "err", err)
			continue
		}
		n += len(chunks)
		all = append(all, chunks...)
		for _, id := range ids {
			idents[id] = struct{}{}
		}
	}
	slog.Info("source repos", "chunks", n, "ms", time.Since(t).Milliseconds())

	t = time.Now()
	n = 0
	for _, co := range checkouts {
		if !co.Repo.Errors {
			continue
		}
		chunks, ids, err := ErrorMessages(co)
		if err != nil {
			warn("errors source failed", "repo", co.Repo.Name, "err", err)
			continue
		}
		n += len(chunks)
		all = append(all, chunks...)
		for _, id := range ids {
			idents[id] = struct{}{}
		}
	}
	slog.Info("source errors", "chunks", n, "identifiers", len(idents), "ms", time.Since(t).Milliseconds())

	t = time.Now()
	ext, err := External(ctx, p.opt.Web, d.Sources.External)
	if err != nil {
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		warn("external source failed", "err", err)
	}
	all = append(all, ext...)
	slog.Info("source external", "chunks", len(ext), "ms", time.Since(t).Milliseconds())

	if p.opt.SeedIssues {
		t = time.Now()
		iss, err := Issues(ctx, p.opt.GitHub, d.Sources.Issues, p.opt.Authenticated)
		if err != nil {
			if ctx.Err() != nil {
				return sum, ctx.Err()
			}
			warn("issues source failed", "err", err)
		}
		all = append(all, iss...)
		slog.Info("source issues", "chunks", len(iss), "rate_remaining", p.opt.GitHub.Remaining(), "ms", time.Since(t).Milliseconds())
	} else {
		slog.Info("source issues skipped (SEED_ISSUES=off)")
	}

	// 3–5. Tag, dedupe, stable IDs.
	Tag(all, d)
	before := len(all)
	all = Dedupe(all)
	slog.Info("dedupe", "before", before, "after", len(all))
	AssignIDs(all)
	for _, c := range all {
		sum.Counts[c.Source]++
	}

	// 6. Embed.
	var emb domain.Embedder
	var vecs [][]float32
	var owners []int
	if p.opt.NewEmbedder != nil {
		emb, vecs, owners = p.embed(ctx, p.opt.NewEmbedder, all, warn, "")
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		if c, ok := emb.(io.Closer); ok {
			defer c.Close()
		}
	}

	// 7. Self-check.
	if err := SelfCheck(ctx, all, d, emb, vecs, owners); err != nil {
		return sum, err
	}

	// 7b. Optional API vectors over the same pieces (failure drops only them).
	var apiEmb domain.Embedder
	var apiVecs [][]float32
	if p.opt.NewAPIEmbedder != nil {
		apiEmb, apiVecs = p.apiVectors(ctx, all, vecs, owners, warn)
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		if c, ok := apiEmb.(io.Closer); ok {
			defer c.Close()
		}
	}

	// 8. Manifest + write.
	m := domain.Manifest{
		BuiltAt: p.opt.Now().UTC(),
		Repos:   sum.Repos,
		Counts:  sum.Counts,
	}
	for id := range idents {
		m.Identifiers = append(m.Identifiers, id)
	}
	sort.Strings(m.Identifiers)
	apiVecs = setEmbedders(&m, emb, vecs, apiEmb, apiVecs)
	if err := index.WriteWithAPI(p.opt.OutDir, m, all, vecs, owners, apiVecs); err != nil {
		return sum, err
	}
	written, err := index.ReadManifest(os.DirFS(p.opt.OutDir))
	if err != nil {
		return sum, fmt.Errorf("read back manifest: %w", err)
	}
	sum.Version = written.Version
	sum.Chunks = len(all)
	sum.Pieces = len(vecs)
	sum.Embedder = m.Embedder
	sum.APIEmbedder = m.APIEmbedder
	sum.Duration = p.opt.Now().Sub(start)
	return sum, nil
}

// apiVectors embeds the pieces with the optional API embedder. The result is
// kept apart from the local vectors so it can never replace them; it is nil
// when there are no local vectors, the embedder fails, the pieces differ or
// its self-check fails.
func (p *Pipeline) apiVectors(ctx context.Context, all []domain.Chunk, vecs [][]float32, owners []int, warn func(string, ...any)) (domain.Embedder, [][]float32) {
	if vecs == nil {
		warn("API embedder skipped: it needs local vectors to fall back to")
		return nil, nil
	}
	e, av, ao := p.embed(ctx, p.opt.NewAPIEmbedder, all, warn, "API ")
	switch {
	case e == nil || ctx.Err() != nil:
		return e, nil
	case !slices.Equal(ao, owners):
		warn("API vectors dropped: pieces differ from the local vectors")
		return e, nil
	}
	if msg := vectorCheck(ctx, all, e, av, ao); msg != "" {
		warn("API vectors dropped: self-check failed", "reason", msg)
		return e, nil
	}
	return e, av
}

// setEmbedders fills m.Embedder from the local embedder and m.APIEmbedder
// from the API one (only alongside local vectors). It returns the API vectors
// to write: nil whenever m.APIEmbedder is not set.
func setEmbedders(m *domain.Manifest, emb domain.Embedder, vecs [][]float32, apiEmb domain.Embedder, apiVecs [][]float32) [][]float32 {
	if emb != nil && vecs != nil {
		info := emb.Info()
		info.Pieces = len(vecs)
		m.Embedder = &info
	}
	if m.Embedder == nil || apiEmb == nil || apiVecs == nil {
		return nil
	}
	info := apiEmb.Info()
	info.Pieces = len(apiVecs)
	m.APIEmbedder = &info
	return apiVecs
}

// embed embeds every piece with the embedder newEmb builds. label prefixes
// the warnings ("" for the local embedder, "API " for the second one).
func (p *Pipeline) embed(ctx context.Context, newEmb func(context.Context) (domain.Embedder, error), chunks []domain.Chunk, warn func(string, ...any), label string) (domain.Embedder, [][]float32, []int) {
	t := time.Now()
	emb, err := newEmb(ctx)
	if err != nil {
		warn(label+"embedder unavailable; continuing without "+label+"vectors", "err", err)
		return nil, nil, nil
	}
	texts, owners := Pieces(chunks)
	vecs := make([][]float32, 0, len(texts))
	for s := 0; s < len(texts); s += tuning.SeedEmbedBatch {
		e := min(s+tuning.SeedEmbedBatch, len(texts))
		out, err := emb.Embed(ctx, texts[s:e])
		if err != nil || len(out) != e-s {
			warn(label+"embedding failed; continuing without "+label+"vectors", "err", err)
			if c, ok := emb.(io.Closer); ok {
				c.Close()
			}
			return nil, nil, nil
		}
		vecs = append(vecs, out...)
	}
	slog.Info("embedded", "provider", emb.Info().Provider, "model", emb.Info().Model, "dim", emb.Info().Dim, "pieces", len(vecs), "ms", time.Since(t).Milliseconds())
	return emb, vecs, owners
}

// AssignIDs sorts chunks by source order, URL, title (stable) and numbers them 0..n-1.
func AssignIDs(chunks []domain.Chunk) {
	rank := func(s domain.SourceKind) int { return slices.Index(domain.SourceOrder, s) }
	sort.SliceStable(chunks, func(i, j int) bool {
		a, b := chunks[i], chunks[j]
		if ra, rb := rank(a.Source), rank(b.Source); ra != rb {
			return ra < rb
		}
		if a.URL != b.URL {
			return a.URL < b.URL
		}
		return a.Title < b.Title
	})
	for i := range chunks {
		chunks[i].ID = i
	}
}
