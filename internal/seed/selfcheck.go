package seed

import (
	"context"
	"fmt"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/search"
	"olake-ask/internal/tuning"
)

var selfCheckQueries = []string{"postgres cdc", "glue catalog", "compaction", "docker compose", "kafka", "helm kubernetes"}

const selfCheckVectorQuery = "how do I set up change data capture for postgres"

// SelfCheck verifies the final chunks are searchable with the server's own
// search code. emb may be nil when there are no vectors.
func SelfCheck(ctx context.Context, chunks []domain.Chunk, d data.Data, emb domain.Embedder, vecs [][]float32, owners []int) error {
	docs := 0
	for _, c := range chunks {
		if c.Source == domain.SourceDocs {
			docs++
		}
	}
	var failed []string
	if docs < tuning.MinDocsChunks {
		failed = append(failed, fmt.Sprintf("docs chunks %d < %d", docs, tuning.MinDocsChunks))
	}
	kw := search.NewKeyword(chunks)
	prep := search.NewPrep(d)
	for _, q := range selfCheckQueries {
		ok := false
		for _, h := range kw.Search(ctx, prep.Prepare(q), tuning.Candidates) {
			if h.Coverage >= tuning.SelfCheckCov {
				ok = true
				break
			}
		}
		if !ok {
			failed = append(failed, fmt.Sprintf("query %q has no hit with coverage ≥ %.1f", q, tuning.SelfCheckCov))
		}
	}
	if msg := vectorCheck(ctx, chunks, emb, vecs, owners); msg != "" {
		failed = append(failed, msg)
	}
	if len(failed) > 0 {
		return fmt.Errorf("self-check failed:\n  - %s", strings.Join(failed, "\n  - "))
	}
	return nil
}

// vectorCheck runs one vector query through the server's vector search and
// returns a failure message, or "" when it passes (or there are no vectors).
func vectorCheck(ctx context.Context, chunks []domain.Chunk, emb domain.Embedder, vecs [][]float32, owners []int) string {
	if emb == nil || len(vecs) == 0 {
		return ""
	}
	dim := len(vecs[0])
	flat := make([]float32, 0, len(vecs)*dim)
	for _, v := range vecs {
		flat = append(flat, v...)
	}
	vs, err := search.NewVector(emb, flat, dim, owners, len(chunks))
	if err != nil {
		return "vector index: " + err.Error()
	}
	hits := vs.Search(ctx, domain.Query{Raw: selfCheckVectorQuery}, 1)
	if len(hits) == 0 || hits[0].Cosine < tuning.SelfCheckCos {
		top := 0.0
		if len(hits) > 0 {
			top = hits[0].Cosine
		}
		return fmt.Sprintf("vector query %q top cosine %.3f < %.1f", selfCheckVectorQuery, top, tuning.SelfCheckCos)
	}
	return ""
}
