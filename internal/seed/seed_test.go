package seed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"olake-ask/internal/client"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/index"
	"olake-ask/internal/tuning"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func findSection(secs []Section, anchor string) *Section {
	for i := range secs {
		if secs[i].Anchor == anchor {
			return &secs[i]
		}
	}
	return nil
}

func TestExtractDocusaurusPostgres(t *testing.T) {
	title, secs, err := ExtractDocusaurus("https://olake.io/docs/connectors/postgres/", readFixture(t, "postgres.html"))
	if err != nil {
		t.Fatal(err)
	}
	// The page h1 is the generic "Overview", so the <title> is used.
	if title != "OLake Postgres Connector: Sync Modes and CDC" {
		t.Fatalf("title = %q", title)
	}
	if len(secs) < 10 || len(secs[0].Heading) != 1 || secs[0].Anchor != "" {
		t.Fatalf("sections = %d, intro = %+v", len(secs), secs[0].Heading)
	}
	sm := findSection(secs, "sync-modes-supported")
	if sm == nil || len(sm.Heading) != 2 || sm.Heading[1] != "Sync Modes Supported" {
		t.Fatalf("h2 section: %+v", sm)
	}
	var danger, tabUI, tabCLI, fence bool
	for _, s := range secs {
		for _, b := range s.Blocks {
			danger = danger || (b.Kind == "note" && strings.HasPrefix(b.Text, "[DANGER] wal2json"))
			tabUI = tabUI || strings.HasPrefix(b.Text, "[Tab: Use OLake UI for Postgres] ")
			tabCLI = tabCLI || strings.HasPrefix(b.Text, "[Tab: Use OLake CLI for Postgres] ")
			if b.Kind == "code" && strings.HasPrefix(b.Text, "```json\n{") && strings.Count(b.Text, "\n") > 5 && strings.HasSuffix(b.Text, "\n```") {
				fence = true
			}
			if strings.Contains(b.Text, "​") || strings.Contains(b.Text, "Direct link to") {
				t.Fatalf("hash-link residue in %q", b.Text)
			}
		}
	}
	if !danger || !tabUI || !tabCLI || !fence {
		t.Fatalf("danger=%v tabUI=%v tabCLI=%v fence=%v", danger, tabUI, tabCLI, fence)
	}
	h3 := findSection(secs, "version-prerequisites")
	if h3 == nil || len(h3.Heading) != 3 || h3.Heading[1] != "Prerequisites" {
		t.Fatalf("h3 section: %+v", h3)
	}
	if SectionURL("https://olake.io/docs/connectors/postgres/", h3.Anchor) != "https://olake.io/docs/connectors/postgres/#version-prerequisites" {
		t.Fatal("section URL")
	}
}

func TestExtractDocusaurusIndex(t *testing.T) {
	title, secs, err := ExtractDocusaurus("https://olake.io/docs/", readFixture(t, "docs_index.html"))
	if err != nil || title != "Welcome to OLake Go" || findSection(secs, "supported-sources") == nil {
		t.Fatalf("title=%q err=%v sections=%d", title, err, len(secs))
	}
}

func TestExtractGeneric(t *testing.T) {
	title, secs, err := ExtractGeneric("https://iceberg.apache.org/docs/latest/maintenance/", readFixture(t, "generic.html"))
	if err != nil {
		t.Fatal(err)
	}
	if title != "Maintenance" || len(secs) != 3 {
		t.Fatalf("title=%q sections=%+v", title, secs)
	}
	if secs[1].Anchor != "expire-snapshots" || secs[2].Anchor != "orphans" || len(secs[2].Heading) != 3 {
		t.Fatalf("anchors: %+v", secs)
	}
	if secs[1].Blocks[1].Text != "```sql\nCALL catalog.system.expire_snapshots('db.tbl');\n```" {
		t.Fatalf("code = %q", secs[1].Blocks[1].Text)
	}
	if secs[2].Blocks[0].Text != "- Run `remove_orphan_files` regularly.\n- Use a safe older_than value." {
		t.Fatalf("list = %q", secs[2].Blocks[0].Text)
	}
	for _, s := range secs {
		for _, b := range s.Blocks {
			if strings.Contains(b.Text, "footer text") || strings.Contains(b.Text, "Top nav") {
				t.Fatalf("chrome not removed: %q", b.Text)
			}
		}
	}
}

func TestExtractMarkdown(t *testing.T) {
	md := strings.Join([]string{
		"---", "title: \"Front Title\"", "---",
		"<!-- hidden", "comment -->",
		"Intro with {{< note >}}shortcode{{< /note >}} and ![img](a.png) <b>bold</b>.",
		"## Setup", "Text one.",
		"```bash", "# not a heading", "## also not", "```",
		"## Setup", "Second setup.",
		"### Sub `x`", "- item 1", "- item 2", "  continued",
		"#### Deep", "| a | b |", "|---|---|", "| 1 | 2 |",
	}, "\n")
	title, secs := ExtractMarkdown(md)
	if title != "Front Title" {
		t.Fatalf("title = %q", title)
	}
	if len(secs) != 4 {
		t.Fatalf("sections = %+v", secs)
	}
	if got := secs[0].Blocks[0].Text; got != "Intro with shortcode and bold." {
		t.Fatalf("intro = %q", got)
	}
	if secs[1].Anchor != "setup" || secs[2].Anchor != "setup-1" || secs[3].Anchor != "sub-x" {
		t.Fatalf("anchors: %q %q %q", secs[1].Anchor, secs[2].Anchor, secs[3].Anchor)
	}
	code := secs[1].Blocks[1]
	if code.Kind != "code" || code.Text != "```bash\n# not a heading\n## also not\n```" {
		t.Fatalf("fence = %+v", code)
	}
	if secs[3].Blocks[0].Kind != "list" || secs[3].Blocks[1].Text != "**Deep**" || secs[3].Blocks[2].Text != "a | b\n1 | 2" {
		t.Fatalf("sub blocks = %+v", secs[3].Blocks)
	}
	if strings.Contains(secs[0].Blocks[0].Text, "hidden") {
		t.Fatal("HTML comment kept")
	}
	if h := WithTitle(title, secs)[3].Heading; !slices.Equal(h, []string{"Front Title", "Setup", "Sub x"}) {
		t.Fatalf("heading = %q", h)
	}
	if title, _ := ExtractMarkdown("# Real Title\n\ntext\n"); title != "Real Title" {
		t.Fatalf("h1 title = %q", title)
	}
}

func words(n int, w string) string { return strings.TrimSpace(strings.Repeat(w+" ", n)) }

func TestChunkLimitsAndOverlap(t *testing.T) {
	blocks := []Block{{Kind: "p", Text: words(200, "alpha")}, {Kind: "p", Text: words(200, "beta")}, {Kind: "p", Text: "tiny"}}
	chunks := Chunk([]Section{{Heading: []string{"T", "H"}, Anchor: "h", Blocks: blocks}}, Meta{PageURL: "https://olake.io/docs/x/", Source: domain.SourceDocs})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for _, c := range chunks {
		if wc(c.Text) > tuning.ChunkMaxWords {
			t.Fatalf("chunk too big: %d", wc(c.Text))
		}
		if c.URL != "https://olake.io/docs/x/#h" || c.Page != "https://olake.io/docs/x/" || c.Title != "T › H" {
			t.Fatalf("chunk meta: %+v", c)
		}
	}
	if !strings.HasPrefix(chunks[1].Text, words(tuning.ChunkOverlapWords, "alpha")) {
		t.Fatalf("overlap missing: %q", chunks[1].Text[:80])
	}
	// Min words drop.
	if got := Chunk([]Section{{Blocks: []Block{{Kind: "p", Text: "too short"}}}}, Meta{}); len(got) != 0 {
		t.Fatalf("short chunk kept: %+v", got)
	}
}

func TestChunkSplitsBigBlocks(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, "key: value two")
	}
	code := Block{Kind: "code", Text: "```yaml\n" + strings.Join(lines, "\n") + "\n```"}
	text := Block{Kind: "p", Text: words(800, "gamma")}
	chunks := Chunk([]Section{{Heading: []string{"T"}, Blocks: []Block{code, text}}}, Meta{})
	fences := 0
	for _, c := range chunks {
		if wc(c.Text) > tuning.ChunkMaxWords {
			t.Fatalf("chunk too big: %d", wc(c.Text))
		}
		if strings.HasPrefix(c.Text, "```") {
			fences++
			if !strings.HasPrefix(c.Text, "```yaml\n") || !strings.HasSuffix(c.Text, "\n```") || strings.Count(c.Text, "```")%2 != 0 {
				t.Fatalf("unbalanced fence: %q…", c.Text[:40])
			}
		}
	}
	if fences < 3 || len(chunks) < 6 {
		t.Fatalf("fences=%d chunks=%d", fences, len(chunks))
	}
}

func TestPieces(t *testing.T) {
	chunks := []domain.Chunk{{ID: 0, Title: "A", Text: words(50, "x")}, {ID: 1, Title: "B", Text: words(300, "y")}}
	texts, owner := Pieces(chunks)
	// 300 words, window 120, step 100 → 0-120, 100-220, 200-300.
	if len(texts) != 4 || !slices.Equal(owner, []int{0, 1, 1, 1}) || !strings.HasPrefix(texts[1], "B\n") {
		t.Fatalf("pieces=%d owner=%v", len(texts), owner)
	}
}

func TestErrorMessages(t *testing.T) {
	co := Checkout{Repo: data.Repo{Name: "olake", URL: "https://github.com/datazip-inc/olake", Product: domain.ProductOLake}, Dir: "testdata/gorepo", SHA: "abc123"}
	chunks, idents, err := ErrorMessages(co)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		var titles []string
		for _, c := range chunks {
			titles = append(titles, c.Title)
		}
		t.Fatalf("chunks = %q", titles)
	}
	var slot *domain.Chunk
	for i := range chunks {
		if strings.Contains(chunks[i].Text, "in func Conn.Slot") && strings.Contains(chunks[i].Title, "failed to get") {
			slot = &chunks[i]
		}
		if strings.Contains(chunks[i].Text, "test file") || strings.Contains(chunks[i].Text, "vendor code") {
			t.Fatal("test or vendor file parsed")
		}
	}
	if slot == nil {
		t.Fatalf("Conn.Slot error missing: %+v", chunks)
	}
	if slot.Source != domain.SourceCode || !strings.HasPrefix(slot.URL, "https://github.com/datazip-inc/olake/blob/abc123/pkg/source/conn.go#L") || !strings.Contains(slot.Text, "```go\n") {
		t.Fatalf("chunk = %+v", slot)
	}
	re := regexp.MustCompile(slot.ErrPattern)
	if !re.MatchString("ERROR: Failed to get replication slot olake_slot: pq: does not exist") {
		t.Fatalf("pattern %q does not match instantiated message", slot.ErrPattern)
	}
	slices.Sort(idents)
	if !slices.Equal(idents, []string{"host", "max_threads", "replication_slot"}) {
		t.Fatalf("identifiers = %v", idents)
	}
}

func TestTagAndDedupe(t *testing.T) {
	d, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	chunks := []domain.Chunk{
		{URL: "https://olake.io/docs/fusion/x/", Title: "Fusion", Text: "same text", Source: domain.SourceDocs},
		{URL: "https://olake.io/docs/connectors/postgres/wal2json/", Title: "Wal", Text: "a", Source: domain.SourceDocs},
		{URL: "https://olake.io/docs/y/", Title: "Old › Deprecated settings", Text: "b", Source: domain.SourceDocs},
		{URL: "https://github.com/datazip-inc/olake-fusion/blob/s/README.md", Title: "r", Text: "c", Source: domain.SourceRepo, Product: domain.ProductFusion},
		{URL: "https://iceberg.apache.org/x/", Title: "e", Text: "d", Source: domain.SourceExternal},
	}
	Tag(chunks, d)
	if chunks[0].Product != domain.ProductFusion || chunks[1].Product != domain.ProductOLake || chunks[3].Product != domain.ProductFusion || chunks[4].Product != domain.ProductNone {
		t.Fatalf("products: %+v", chunks)
	}
	if chunks[0].Deprecated || !chunks[1].Deprecated || !chunks[2].Deprecated {
		t.Fatalf("deprecated flags: %+v", chunks)
	}
	dup := []domain.Chunk{
		{URL: "issue", Text: "Same  TEXT", Source: domain.SourceIssue},
		{URL: "docs", Text: "same text", Source: domain.SourceDocs},
		{URL: "repo1", Text: "other", Source: domain.SourceRepo},
		{URL: "repo2", Text: "other", Source: domain.SourceRepo},
	}
	got := Dedupe(dup)
	if len(got) != 2 || got[0].URL != "docs" || got[1].URL != "repo1" {
		t.Fatalf("dedupe = %+v", got)
	}
	// Copies on different pages are kept (search drops repeats at query time).
	cross := []domain.Chunk{
		{URL: "https://olake.io/docs/a/#s", Page: "https://olake.io/docs/a/", Text: "Create publication", Source: domain.SourceDocs},
		{URL: "https://olake.io/docs/b/#s", Page: "https://olake.io/docs/b/", Text: "create  publication", Source: domain.SourceDocs},
		{URL: "https://olake.io/docs/a/#t", Page: "https://olake.io/docs/a/", Text: "create publication", Source: domain.SourceDocs},
	}
	if got := Dedupe(cross); len(got) != 2 || got[0].Page == got[1].Page {
		t.Fatalf("cross-page dedupe = %+v", got)
	}
	low := []domain.Chunk{
		{URL: "https://olake.io/docs/community/contributing/#orientation", Source: domain.SourceDocs},
		{URL: "https://olake.io/docs/community/contributing/#other", Source: domain.SourceDocs},
	}
	Tag(low, d)
	if !low[0].LowValue || low[1].LowValue {
		t.Fatalf("low value flags: %+v", low)
	}
}

func TestFilterDocURLs(t *testing.T) {
	cfg := data.Docs{IncludePrefixes: []string{"https://olake.io/docs/"}, ExcludeSubstrings: []string{"/docs/tags/"}}
	got := FilterDocURLs([]string{
		"https://olake.io/docs/b?x=1#f", "https://olake.io/docs/a/", "https://olake.io/docs/b/",
		"https://olake.io/blog/x/", "https://olake.io/docs/tags/k/",
	}, cfg)
	if !slices.Equal(got, []string{"https://olake.io/docs/a/", "https://olake.io/docs/b/"}) {
		t.Fatalf("urls = %v", got)
	}
}

func TestValuesChunksAndKeyPaths(t *testing.T) {
	yml := "# header\n\n# Global settings\nglobal:\n  jobMapping: {}\n  image:\n    tag: v1\n# Worker\nworker:\n  replicas: 2\n"
	co := Checkout{Repo: data.Repo{Name: "olake-helm", URL: "https://github.com/datazip-inc/olake-helm"}, SHA: "s"}
	chunks := ValuesChunks(co, "helm/olake/values.yaml", yml)
	if len(chunks) != 1 || chunks[0].URL != "https://github.com/datazip-inc/olake-helm/blob/s/helm/olake/values.yaml#L1" ||
		chunks[0].Title != "olake-helm: helm/olake/values.yaml › global" {
		t.Fatalf("chunks = %+v", chunks) // the short worker section falls under ChunkMinWords
	}
	for _, c := range chunks {
		if !strings.HasPrefix(c.Text, "```yaml\n") {
			t.Fatalf("not fenced: %q", c.Text)
		}
	}
	paths := YAMLKeyPaths([]byte(yml))
	for _, want := range []string{"global", "global.jobMapping", "global.image.tag", "worker.replicas"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("paths %v missing %s", paths, want)
		}
	}
}

func TestTrimIssueText(t *testing.T) {
	var code []string
	for i := 0; i < 30; i++ {
		code = append(code, "line")
	}
	code[25] = "ERROR: boom"
	s := "Hello\n\n\n\nworld\n```\n" + strings.Join(code, "\n") + "\n```"
	got := trimIssueText(s, 10000)
	if strings.Count(got, "line") != 15 || !strings.Contains(got, "ERROR: boom") || strings.Contains(got, "\n\n\n") {
		t.Fatalf("trim = %q", got)
	}
	if got := trimIssueText(strings.Repeat("word ", 100), 50); len(got) > 55 {
		t.Fatalf("cap = %d", len(got))
	}
	cs := IssueChunks("datazip-inc/olake", client.Issue{Number: 7, Title: "Slot error", Body: "my password=hunter2", HTMLURL: "https://github.com/datazip-inc/olake/issues/7"}, []string{"Fixed in v1"}, domain.ProductOLake)
	if len(cs) != 1 || cs[0].Title != "GitHub issue #7 (datazip-inc/olake): Slot error" || strings.Contains(cs[0].Text, "hunter2") {
		t.Fatalf("issue chunk = %+v", cs)
	}
}

// constEmb returns the same vector for every text, so its output is recognizable.
type constEmb struct {
	provider string
	vec      []float32
	err      error
}

func (c constEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = slices.Clone(c.vec)
	}
	return out, nil
}
func (c constEmb) Info() domain.EmbedInfo {
	return domain.EmbedInfo{Provider: c.provider, Model: c.provider + "-model", Dim: len(c.vec)}
}

// The local and API vectors must never be swapped: the manifest's embedder,
// vectors.bin, api_embedder and vectors_api.bin must each match their source.
func TestSeedVectorsNotMixedUp(t *testing.T) {
	chunks := []domain.Chunk{{ID: 0, Title: "A", Text: words(50, "x")}, {ID: 1, Title: "B", Text: words(300, "y")}}
	local := constEmb{provider: "model2vec", vec: []float32{1, 0}}
	// Same dimension on purpose: only the values tell the sets apart.
	api := constEmb{provider: "cohere", vec: []float32{0, 1}}
	p := NewPipeline(Options{
		NewEmbedder:    func(context.Context) (domain.Embedder, error) { return local, nil },
		NewAPIEmbedder: func(context.Context) (domain.Embedder, error) { return api, nil },
	})
	var warns []string
	warn := func(msg string, _ ...any) { warns = append(warns, msg) }
	ctx := context.Background()

	emb, vecs, owners := p.embed(ctx, p.opt.NewEmbedder, chunks, warn, "")
	apiEmb, apiVecs := p.apiVectors(ctx, chunks, vecs, owners, warn)
	var m domain.Manifest
	apiVecs = setEmbedders(&m, emb, vecs, apiEmb, apiVecs)
	dir := filepath.Join(t.TempDir(), "index")
	if err := index.WriteWithAPI(dir, m, chunks, vecs, owners, apiVecs); err != nil {
		t.Fatal(err)
	}
	b, err := index.Read(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if e := b.Manifest.Embedder; e == nil || e.Provider != "model2vec" || e.Dim != 2 || e.Pieces != len(owners) {
		t.Fatalf("manifest embedder = %+v", e)
	}
	for i := 0; i < len(b.Vectors); i += 2 {
		if b.Vectors[i] != 1 || b.Vectors[i+1] != 0 {
			t.Fatalf("vectors.bin holds non-local vectors: %v", b.Vectors)
		}
	}
	if e := b.Manifest.APIEmbedder; e == nil || e.Provider != "cohere" || e.Dim != 2 {
		t.Fatalf("manifest api_embedder = %+v", e)
	}
	for i := 0; i < len(b.APIVectors); i += 2 {
		if b.APIVectors[i] != 0 || b.APIVectors[i+1] != 1 {
			t.Fatalf("vectors_api.bin holds non-API vectors: %v", b.APIVectors)
		}
	}
}

func TestSeedAPIFailureKeepsLocalVectors(t *testing.T) {
	chunks := []domain.Chunk{{ID: 0, Title: "A", Text: words(50, "x")}}
	local := constEmb{provider: "model2vec", vec: []float32{1, 0}}
	ctx := context.Background()
	for name, api := range map[string]func(context.Context) (domain.Embedder, error){
		"unavailable": func(context.Context) (domain.Embedder, error) { return nil, errors.New("no key") },
		"embed error": func(context.Context) (domain.Embedder, error) {
			return constEmb{provider: "cohere", err: errors.New("429")}, nil
		},
		// Zero vectors: the self-check query scores cosine 0 < SelfCheckCos.
		"self-check": func(context.Context) (domain.Embedder, error) {
			return constEmb{provider: "cohere", vec: []float32{0, 0, 0}}, nil
		},
	} {
		p := NewPipeline(Options{NewAPIEmbedder: api})
		var warns []string
		warn := func(msg string, _ ...any) { warns = append(warns, msg) }
		emb, vecs, owners := p.embed(ctx, func(context.Context) (domain.Embedder, error) { return local, nil }, chunks, warn, "")
		apiEmb, apiVecs := p.apiVectors(ctx, chunks, vecs, owners, warn)
		var m domain.Manifest
		apiVecs = setEmbedders(&m, emb, vecs, apiEmb, apiVecs)
		if apiVecs != nil || m.APIEmbedder != nil || m.Embedder == nil || m.Embedder.Provider != "model2vec" || len(vecs) != len(owners) {
			t.Errorf("%s: api=%v manifest=%+v/%+v", name, apiVecs, m.Embedder, m.APIEmbedder)
		}
		if len(warns) == 0 {
			t.Errorf("%s: no warning", name)
		}
	}
	// No local vectors: the API set is skipped entirely.
	p := NewPipeline(Options{NewAPIEmbedder: func(context.Context) (domain.Embedder, error) {
		t.Fatal("API embedder must not run without local vectors")
		return nil, nil
	}})
	if e, v := p.apiVectors(ctx, chunks, nil, nil, func(string, ...any) {}); e != nil || v != nil {
		t.Fatal("API vectors without local vectors")
	}
}

// A short leading chunk ("The table below shows…") joins the next block
// instead of becoming a chunk of its own.
func TestChunkMergesShortLeadIn(t *testing.T) {
	blocks := []Block{{Kind: "p", Text: words(20, "intro")}, {Kind: "p", Text: words(tuning.ChunkMaxWords-5, "table")}}
	got := chunkBlocks(blocks)
	if len(got) != 1 || !strings.HasPrefix(got[0], "intro") || !strings.Contains(got[0], "table") {
		t.Fatalf("chunks = %d: %.60q", len(got), got)
	}
	// A chunk at or above ChunkMergeWords still splits as before.
	blocks[0].Text = words(tuning.ChunkMergeWords, "intro")
	if got := chunkBlocks(blocks); len(got) != 2 {
		t.Fatalf("long lead-in must split, got %d", len(got))
	}
}

// Table symbols are not embedded; the words of the table are.
func TestPiecesDropSymbolTokens(t *testing.T) {
	texts, _ := Pieces([]domain.Chunk{{ID: 0, Title: "T", Text: "Source | Full Refresh\nPostgreSQL | ✅ | — | ✅\nMSSQL | ✅"}})
	if len(texts) != 1 || texts[0] != "T\nSource Full Refresh PostgreSQL MSSQL" {
		t.Fatalf("pieces = %q", texts)
	}
}
