package index

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"olake-ask/internal/domain"
)

func fixture() ([]domain.Chunk, domain.Manifest) {
	chunks := []domain.Chunk{
		{ID: 0, URL: "https://olake.io/docs/a/#x", Page: "https://olake.io/docs/a/", Title: "A › X", Text: "alpha", Source: domain.SourceDocs, Product: domain.ProductOLake},
		{ID: 1, URL: "https://olake.io/docs/b/", Page: "https://olake.io/docs/b/", Title: "B", Text: "beta <tag> & co", Source: domain.SourceDocs, Deprecated: true},
	}
	m := domain.Manifest{BuiltAt: time.Date(2026, 10, 17, 10, 30, 0, 0, time.UTC), Repos: map[string]string{"olake": "abc"},
		Counts: map[domain.SourceKind]int{domain.SourceDocs: 2}, Identifiers: []string{"a.b"}}
	return chunks, m
}

func TestRoundTripNoVectors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	chunks, m := fixture()
	if err := Write(dir, m, chunks, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := Read(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.Chunks, chunks) || b.Manifest.Embedder != nil || b.Manifest.Chunks != 2 {
		t.Fatalf("round trip mismatch: %+v", b)
	}
	if len(b.Manifest.Version) != len("20261017T1030Z-")+8 {
		t.Fatalf("version = %q", b.Manifest.Version)
	}
	if _, err := os.Stat(filepath.Join(dir, VectorsFile)); !os.IsNotExist(err) {
		t.Fatal("vectors.bin must not exist without embedder")
	}
}

func TestRoundTripVectorsAndStaleRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, readmeFile), []byte("keep"), 0o644)
	os.WriteFile(filepath.Join(dir, "stale.bin"), []byte("x"), 0o644)
	chunks, m := fixture()
	m.Embedder = &domain.EmbedInfo{Provider: "model2vec", Model: "BASE8M", Dim: 2}
	vecs := [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}}
	if err := Write(dir, m, chunks, vecs, []int{0, 1, 1}); err != nil {
		t.Fatal(err)
	}
	b, err := Read(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.Vectors, []float32{1, 0, 0, 1, 0.6, 0.8}) || !reflect.DeepEqual(b.Pieces, []int{0, 1, 1}) || b.Manifest.Embedder.Pieces != 3 {
		t.Fatalf("vectors: %+v", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.bin")); !os.IsNotExist(err) {
		t.Fatal("stale file kept")
	}
	if _, err := os.Stat(filepath.Join(dir, readmeFile)); err != nil {
		t.Fatal("README removed")
	}
	if _, err := os.Stat(dir + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp dir left behind")
	}
	// Re-write without vectors removes vector files.
	m.Embedder = nil
	if err := Write(dir, m, chunks, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, VectorsFile)); !os.IsNotExist(err) {
		t.Fatal("old vectors kept")
	}
}

func TestInvalidBundlesRejected(t *testing.T) {
	chunks, m := fixture()
	m.Embedder = &domain.EmbedInfo{Provider: "model2vec", Model: "BASE8M", Dim: 2}
	dir := filepath.Join(t.TempDir(), "index")
	if err := Write(dir, m, chunks, [][]float32{{1, 0}}, []int{0}); err != nil {
		t.Fatal(err)
	}
	// Truncated vectors.
	os.WriteFile(filepath.Join(dir, VectorsFile), []byte{0, 0, 0, 0}, 0o644)
	if _, err := Read(os.DirFS(dir)); err == nil {
		t.Fatal("truncated vectors accepted")
	}
	// Missing chunks.
	os.Remove(filepath.Join(dir, ChunksFile))
	if _, err := Read(os.DirFS(dir)); err == nil {
		t.Fatal("missing chunks accepted")
	}
	// Empty dir (only README).
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, readmeFile), []byte("x"), 0o644)
	if _, err := Read(os.DirFS(empty)); err == nil {
		t.Fatal("empty bundle accepted")
	}
	// Non-dense IDs are refused at write time.
	chunks[1].ID = 5
	if err := Write(filepath.Join(t.TempDir(), "x"), m, chunks, nil, nil); err == nil {
		t.Fatal("non-dense ids accepted")
	}
}

func TestRoundTripAPIVectors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	chunks, m := fixture()
	m.Embedder = &domain.EmbedInfo{Provider: "model2vec", Model: "RETRIEVAL32M", Dim: 2}
	m.APIEmbedder = &domain.EmbedInfo{Provider: "cohere", Model: "embed-v4.0", Dim: 3, OutputDim: 3}
	local := [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}}
	api := [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	if err := WriteWithAPI(dir, m, chunks, local, []int{0, 1, 1}, api); err != nil {
		t.Fatal(err)
	}
	b, err := Read(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.Vectors, []float32{1, 0, 0, 1, 0.6, 0.8}) || b.Manifest.Embedder.Model != "RETRIEVAL32M" {
		t.Fatalf("local vectors changed: %+v", b)
	}
	if !reflect.DeepEqual(b.APIVectors, []float32{1, 0, 0, 0, 1, 0, 0, 0, 1}) || b.Manifest.APIEmbedder.Pieces != 3 || b.Manifest.APIEmbedder.OutputDim != 3 {
		t.Fatalf("api vectors: %+v %+v", b.APIVectors, b.Manifest.APIEmbedder)
	}

	// A broken API set is dropped; the local index still loads.
	os.WriteFile(filepath.Join(dir, APIVectorsFile), []byte{0, 0, 0, 0}, 0o644)
	b, err = Read(os.DirFS(dir))
	if err != nil || b.APIVectors != nil || b.Manifest.APIEmbedder != nil || len(b.Vectors) != 6 {
		t.Fatalf("broken API set: err=%v api=%v manifest=%+v", err, b.APIVectors, b.Manifest.APIEmbedder)
	}
	os.Remove(filepath.Join(dir, APIVectorsFile))
	if b, err = Read(os.DirFS(dir)); err != nil || b.APIVectors != nil || len(b.Vectors) != 6 {
		t.Fatalf("missing API file: err=%v", err)
	}

	// Plain Write never writes API vectors, even if the manifest names some.
	if err := Write(dir, m, chunks, local, []int{0, 1, 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, APIVectorsFile)); !os.IsNotExist(err) {
		t.Fatal("Write left API vectors")
	}
	if b, err = Read(os.DirFS(dir)); err != nil || b.Manifest.APIEmbedder != nil {
		t.Fatalf("Write kept api_embedder: %v %+v", err, b.Manifest.APIEmbedder)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	if strings.Contains(string(raw), "api_embedder") || strings.Contains(string(raw), "output_dim") {
		t.Fatalf("local-only manifest must not mention API fields:\n%s", raw)
	}
}

func TestWriteWithAPIRejectsInconsistentSets(t *testing.T) {
	chunks, m := fixture()
	local := [][]float32{{1, 0}}
	dir := filepath.Join(t.TempDir(), "index")
	if err := WriteWithAPI(dir, m, chunks, nil, nil, [][]float32{{1}}); err == nil {
		t.Fatal("API vectors without api_embedder accepted")
	}
	m.APIEmbedder = &domain.EmbedInfo{Provider: "cohere", Model: "x", Dim: 1}
	if err := WriteWithAPI(dir, m, chunks, nil, nil, [][]float32{{1}}); err == nil {
		t.Fatal("API vectors without local vectors accepted")
	}
	m.Embedder = &domain.EmbedInfo{Provider: "model2vec", Model: "RETRIEVAL32M", Dim: 2}
	if err := WriteWithAPI(dir, m, chunks, local, []int{0}, [][]float32{{1}, {1}}); err == nil {
		t.Fatal("API/piece count mismatch accepted")
	}
	if err := WriteWithAPI(dir, m, chunks, local, []int{0}, [][]float32{{1, 2}}); err == nil {
		t.Fatal("API dim mismatch accepted")
	}
}

// The committed bundle must keep loading unchanged.
func TestCommittedBundleReads(t *testing.T) {
	b, err := Read(os.DirFS(filepath.Join("..", "..", "assets", "index")))
	if err != nil {
		t.Skipf("no committed bundle: %v", err)
	}
	if b.Manifest.Embedder == nil || len(b.Vectors) != b.Manifest.Embedder.Pieces*b.Manifest.Embedder.Dim {
		t.Fatalf("committed bundle vectors: %+v", b.Manifest.Embedder)
	}
}
