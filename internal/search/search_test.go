package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

func loadData(t *testing.T) data.Data {
	t.Helper()
	d, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mk(id int, page, title, text string, src domain.SourceKind, prod domain.Product) domain.Chunk {
	return domain.Chunk{ID: id, URL: page, Page: page, Title: title, Text: text, Source: src, Product: prod}
}

func fixtureChunks() []domain.Chunk {
	return []domain.Chunk{
		mk(0, "https://olake.io/docs/a/", "Kafka source", "configure brokers and topics for ingestion", domain.SourceDocs, domain.ProductOLake),
		mk(1, "https://olake.io/docs/b/", "Brokers", "the kafka connector reads topics from brokers", domain.SourceDocs, domain.ProductOLake),
		mk(2, "https://olake.io/docs/fusion/c/", "Self-optimizing tables", "fusion runs self-optimizing jobs that merge small data files", domain.SourceDocs, domain.ProductFusion),
		mk(3, "https://olake.io/docs/d/", "Postgres CDC", "enable logical replication with pgoutput and a replication slot", domain.SourceDocs, domain.ProductOLake),
	}
}

func TestPrepareSynonymsProductFlags(t *testing.T) {
	p := NewPrep(loadData(t))
	q := p.Prepare("How does compaction work?")
	if q.Product != domain.ProductFusion {
		t.Fatalf("product = %q", q.Product)
	}
	var syn bool
	for _, term := range q.Terms {
		if term.Text == "optimizing" && term.Weight == tuning.SynonymWeight {
			syn = true
		}
	}
	if !syn || len(q.Content) != 2 {
		t.Fatalf("terms = %+v content=%v", q.Terms, q.Content)
	}
	if q := p.Prepare("sync postgres with fusion compaction"); q.Product != domain.ProductNone {
		t.Fatalf("mixed product = %q", q.Product)
	}
	if q := p.Prepare("sync failed: connection refused by host"); !q.ErrorLike {
		t.Fatal("error-like not detected")
	}
	if q := p.Prepare("error"); q.ErrorLike {
		t.Fatal("short text must not be error-like")
	}
	if q := p.Prepare("как настроить репликацию"); !q.NonEnglish {
		t.Fatal("non-English not detected")
	}
}

func TestKeywordTitleAboveBody(t *testing.T) {
	chunks := fixtureChunks()
	k := NewKeyword(chunks)
	p := NewPrep(loadData(t))
	hits := k.Search(context.Background(), p.Prepare("kafka"), 10)
	if len(hits) < 2 || hits[0].ChunkID != 0 {
		t.Fatalf("hits = %+v", hits)
	}
	if k.Search(context.Background(), domain.Query{}, 10) != nil {
		t.Fatal("empty query must return nil")
	}
}

func TestKeywordCoverage(t *testing.T) {
	k := NewKeyword(fixtureChunks())
	if c := k.Coverage(3, []string{"postgre", "cdc", "mongodb"}); math.Abs(c-2.0/3) > 1e-9 {
		t.Fatalf("coverage = %v", c)
	}
	if k.Coverage(3, nil) != 0 {
		t.Fatal("empty content coverage must be 0")
	}
}

func TestSynonymExpansionMatchesSelfOptimizing(t *testing.T) {
	k := NewKeyword(fixtureChunks())
	p := NewPrep(loadData(t))
	hits := k.Search(context.Background(), p.Prepare("compaction"), 5)
	if len(hits) == 0 || hits[0].ChunkID != 2 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestFuseRRFAndBoosts(t *testing.T) {
	chunks := fixtureChunks()
	lists := [][]domain.Hit{
		{{ChunkID: 0, Coverage: 0.5}, {ChunkID: 1}},
		{{ChunkID: 1, Cosine: 0.7}},
	}
	got := Fuse(lists, chunks, domain.Query{}, 10, nil)
	want1 := 1/(tuning.RRFK+2) + 1/(tuning.RRFK+1)
	if got[0].ChunkID != 1 || math.Abs(got[0].Score-want1) > 1e-12 || got[0].Cosine != 0.7 {
		t.Fatalf("fused = %+v", got)
	}
	if math.Abs(got[1].Score-1/(tuning.RRFK+1)) > 1e-12 || got[1].Coverage != 0.5 {
		t.Fatalf("fused[1] = %+v", got[1])
	}

	// Product boost lifts the fusion chunk over an equal-rank olake chunk.
	lists = [][]domain.Hit{{{ChunkID: 0}}, {{ChunkID: 2}}}
	got = Fuse(lists, chunks, domain.Query{Product: domain.ProductFusion}, 10, nil)
	if got[0].ChunkID != 2 || math.Abs(got[0].Score/got[1].Score-tuning.ProductBoost) > 1e-9 {
		t.Fatalf("boost = %+v", got)
	}

	// Source weight.
	chunks[0].Source = domain.SourceIssue
	got = Fuse([][]domain.Hit{{{ChunkID: 0}}}, chunks, domain.Query{}, 10, nil)
	if math.Abs(got[0].Score-tuning.SourceWeight[domain.SourceIssue]/(tuning.RRFK+1)) > 1e-12 {
		t.Fatalf("source weight = %+v", got)
	}
}

func TestFuseDeprecatedPenalty(t *testing.T) {
	chunks := fixtureChunks()
	chunks[3].Deprecated = true
	chunks[3].URL = "https://olake.io/docs/connectors/postgres/wal2json/"
	dep := []data.Deprecated{{Term: "wal2json", Pages: []string{"/docs/connectors/postgres/wal2json"}}}
	lists := [][]domain.Hit{{{ChunkID: 3}}}
	base := 1 / (tuning.RRFK + 1)
	if got := Fuse(lists, chunks, domain.Query{Mentions: "postgres cdc"}, 10, dep); math.Abs(got[0].Score-base*tuning.DeprecatedPenalty) > 1e-12 {
		t.Fatalf("penalty not applied: %+v", got)
	}
	if got := Fuse(lists, chunks, domain.Query{Mentions: "use wal2json"}, 10, dep); math.Abs(got[0].Score-base) > 1e-12 {
		t.Fatalf("term mention should waive penalty: %+v", got)
	}
	if got := Fuse(lists, chunks, domain.Query{Mentions: "is it deprecated"}, 10, dep); math.Abs(got[0].Score-base) > 1e-12 {
		t.Fatalf("deprecat mention should waive penalty: %+v", got)
	}
}

func TestFusePerPageCap(t *testing.T) {
	page := "https://olake.io/docs/same/"
	var chunks []domain.Chunk
	var list []domain.Hit
	for i := 0; i < 5; i++ {
		chunks = append(chunks, mk(i, page, "t", fmt.Sprint("x", i), domain.SourceDocs, ""))
		list = append(list, domain.Hit{ChunkID: i})
	}
	chunks = append(chunks, mk(5, "https://olake.io/docs/other/", "t", "x5", domain.SourceDocs, ""))
	list = append(list, domain.Hit{ChunkID: 5})
	got := Fuse([][]domain.Hit{list}, chunks, domain.Query{}, 10, nil)
	if len(got) != tuning.PerPageCap+1 || got[tuning.PerPageCap].ChunkID != 5 {
		t.Fatalf("cap = %+v", got)
	}
	for i := 0; i < tuning.PerPageCap; i++ {
		if got[i].ChunkID != i {
			t.Fatalf("cap = %+v", got)
		}
	}
	if got := Fuse([][]domain.Hit{list}, chunks, domain.Query{}, 1, nil); len(got) != 1 {
		t.Fatalf("limit = %+v", got)
	}
}

func TestErrorsSearcher(t *testing.T) {
	pat := utils.ErrorPattern(utils.ErrorPieces("failed to get replication slot %s: %v"))
	chunks := []domain.Chunk{
		{ID: 0, Title: "olake error: failed to get replication slot %s: %v", ErrPattern: pat},
		{ID: 1, Title: "other", ErrPattern: utils.ErrorPattern(utils.ErrorPieces("unrelated failure in writer %d"))},
		{ID: 2, Title: "no pattern"},
	}
	e := NewErrors(chunks)
	if e.Len() != 2 {
		t.Fatalf("len = %d", e.Len())
	}
	p := NewPrep(loadData(t))
	q := p.Prepare("I get: Failed to get replication slot olake_slot: pq: connection refused")
	hits := e.Search(context.Background(), q, 5)
	if len(hits) != 1 || hits[0].ChunkID != 0 {
		t.Fatalf("hits = %+v", hits)
	}
	q.ErrorLike = false
	if e.Search(context.Background(), q, 5) != nil {
		t.Fatal("non error-like query must not match")
	}
}

type fakeEmb struct {
	vec []float32
	err error
}

func (f fakeEmb) Embed(context.Context, []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return [][]float32{f.vec}, nil
}
func (f fakeEmb) Info() domain.EmbedInfo { return domain.EmbedInfo{Provider: "fake", Dim: len(f.vec)} }

func TestVectorSearch(t *testing.T) {
	vecs := []float32{1, 0, 0, 1, 0.6, 0.8}
	v, err := NewVector(fakeEmb{vec: []float32{0, 1}}, vecs, 2, []int{0, 1, 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	hits := v.Search(context.Background(), domain.Query{Raw: "q"}, 5)
	if len(hits) != 2 || hits[0].ChunkID != 1 || hits[0].Cosine != 1 || hits[1].Cosine != 0 {
		t.Fatalf("hits = %+v", hits)
	}
	if _, err := NewVector(fakeEmb{vec: []float32{0, 1, 0}}, vecs, 2, []int{0, 1, 1}, 2); err == nil {
		t.Fatal("dim mismatch accepted")
	}
	v, _ = NewVector(fakeEmb{vec: []float32{0, 1}, err: errors.New("boom")}, vecs, 2, []int{0, 1, 1}, 2)
	if v.Search(context.Background(), domain.Query{Raw: "q"}, 5) != nil {
		t.Fatal("embed error must return nil")
	}
}

func TestRetriever(t *testing.T) {
	chunks := fixtureChunks()
	d := loadData(t)
	vecs := []float32{0, 1, 0, 1, 0, 1, 1, 0}
	v, err := NewVector(fakeEmb{vec: []float32{1, 0}}, vecs, 2, []int{0, 1, 2, 3}, len(chunks))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(NewPrep(d), chunks, NewKeyword(chunks), v, NewErrors(chunks), d.Deprecated)
	ret := r.Retrieve(context.Background(), r.Prep().Prepare("postgres cdc replication slot"), []string{"kafka brokers"})
	if len(ret.Hits) == 0 || ret.Hits[0].ChunkID != 3 || ret.MaxCoverage != 1 || ret.MaxCosine != 1 || ret.ErrHits != 0 {
		t.Fatalf("retrieval = %+v", ret)
	}
	if !r.VectorsOn() || r.Chunk(2).ID != 2 || len(r.Chunks()) != 4 {
		t.Fatal("accessors")
	}
}

type stubSearcher struct {
	hits  []domain.Hit
	calls *int
}

func (s stubSearcher) Name() string { return "stub" }
func (s stubSearcher) Search(context.Context, domain.Query, int) []domain.Hit {
	*s.calls++
	return s.hits
}

func TestFallback(t *testing.T) {
	var pc, sc int
	ok := stubSearcher{hits: []domain.Hit{{ChunkID: 1}}, calls: &pc}
	sec := stubSearcher{hits: []domain.Hit{{ChunkID: 2}}, calls: &sc}
	q := domain.Query{Raw: "q"}
	if h := NewFallback(ok, sec).Search(context.Background(), q, 5); len(h) != 1 || h[0].ChunkID != 1 || sc != 0 {
		t.Fatalf("primary ok: hits=%v secondary calls=%d", h, sc)
	}
	failed := stubSearcher{calls: &pc}
	if h := NewFallback(failed, sec).Search(context.Background(), q, 5); len(h) != 1 || h[0].ChunkID != 2 || sc != 1 {
		t.Fatalf("primary failed: hits=%v secondary calls=%d", h, sc)
	}
	if h := NewFallback(failed, sec).Search(context.Background(), domain.Query{}, 5); h != nil || sc != 1 {
		t.Fatal("empty query must not fall back")
	}
}

func TestVectorStampsFloorAndTimeout(t *testing.T) {
	vecs := []float32{1, 0, 0, 1}
	v, _ := NewVector(fakeEmb{vec: []float32{0, 1}}, vecs, 2, []int{0, 1}, 2)
	if h := v.Search(context.Background(), domain.Query{Raw: "q"}, 5); h[0].CosineFloor != 0 {
		t.Fatalf("default floor must be 0 (= tuning.FloorCosine), got %v", h[0].CosineFloor)
	}
	v.SetFloor(0.3).SetTimeout(time.Second)
	if h := v.Search(context.Background(), domain.Query{Raw: "q"}, 5); h[0].CosineFloor != 0.3 || h[1].CosineFloor != 0.3 || v.timeout != time.Second {
		t.Fatalf("floor/timeout not applied: %+v", h)
	}
}

// With several vector lists from models with different floors, the retrieval
// reports the list with the most headroom over its own floor.
func TestRetrieverPerModelFloor(t *testing.T) {
	chunks := fixtureChunks()
	d := loadData(t)
	vecs := []float32{0, 1, 0, 1, 0, 1, 1, 0}
	v, _ := NewVector(fakeEmb{vec: []float32{0.6, 0.8}}, vecs, 2, []int{0, 1, 2, 3}, len(chunks))
	r := NewRetriever(NewPrep(d), chunks, NewKeyword(chunks), v.SetFloor(0.7), NewErrors(chunks), d.Deprecated)
	ret := r.Retrieve(context.Background(), r.Prep().Prepare("unrelated words"), nil)
	if math.Abs(ret.MaxCosine-0.8) > 1e-6 || ret.CosineFloor != 0.7 {
		t.Fatalf("max=%v floor=%v", ret.MaxCosine, ret.CosineFloor)
	}
	// No positive cosine: same as before per-model floors (0 at the default floor).
	neg, _ := NewVector(fakeEmb{vec: []float32{-1, 0}}, []float32{1, 0}, 2, []int{0}, len(chunks))
	r = NewRetriever(NewPrep(d), chunks, NewKeyword(chunks), neg, NewErrors(chunks), d.Deprecated)
	if ret := r.Retrieve(context.Background(), r.Prep().Prepare("x"), nil); ret.MaxCosine != 0 || ret.CosineFloor != 0 {
		t.Fatalf("negative cosines: max=%v floor=%v", ret.MaxCosine, ret.CosineFloor)
	}
}

// The same text on several pages (shared setup steps) is shown once: the
// best-ranked copy wins, and the page cap still applies per page.
func TestFuseDropsRepeatedText(t *testing.T) {
	chunks := []domain.Chunk{
		mk(0, "https://olake.io/docs/a/", "A", "Create  Publication", domain.SourceDocs, ""),
		mk(1, "https://olake.io/docs/b/", "B", "create publication", domain.SourceDocs, ""),
		mk(2, "https://olake.io/docs/c/", "C", "other", domain.SourceDocs, ""),
	}
	list := []domain.Hit{{ChunkID: 1}, {ChunkID: 0}, {ChunkID: 2}}
	got := Fuse([][]domain.Hit{list}, chunks, domain.Query{}, 10, nil)
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Fatalf("dedupe = %+v", got)
	}
}

func TestFuseLowValuePenalty(t *testing.T) {
	chunks := []domain.Chunk{
		mk(0, "https://olake.io/docs/community/contributing/", "Orientation", "repos", domain.SourceDocs, ""),
		mk(1, "https://olake.io/docs/x/", "X", "answer", domain.SourceDocs, ""),
	}
	chunks[0].LowValue = true
	got := Fuse([][]domain.Hit{{{ChunkID: 0}, {ChunkID: 1}}}, chunks, domain.Query{}, 10, nil)
	if got[0].ChunkID != 1 || math.Abs(got[1].Score-tuning.LowValuePenalty/(tuning.RRFK+1)) > 1e-12 {
		t.Fatalf("low value = %+v", got)
	}
}
