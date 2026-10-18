package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"olake-ask/assets"
	"olake-ask/internal/api"
	"olake-ask/internal/ask"
	"olake-ask/internal/client"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/embed"
	"olake-ask/internal/index"
	"olake-ask/internal/middleware"
	"olake-ask/internal/search"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// LoadBundle reads the index bundle compiled into the binary.
func LoadBundle() (index.Bundle, error) {
	fsys, err := assets.Index()
	if err != nil {
		return index.Bundle{}, fmt.Errorf(`index bundle missing or invalid — run "make seed" and rebuild: %w`, err)
	}
	b, err := index.Read(fsys)
	if err != nil {
		return index.Bundle{}, fmt.Errorf(`index bundle missing or invalid — run "make seed" and rebuild: %w`, err)
	}
	return b, nil
}

// NewRetriever builds the retriever (keyword, errors and, when possible, vectors).
func NewRetriever(ctx context.Context, cfg config.Server, d data.Data, b index.Bundle) *search.Retriever {
	var vec domain.Searcher
	local := newLocalVector(ctx, cfg, b)
	api := newAPIVector(cfg, b)
	switch {
	case api != nil && local != nil:
		vec = search.NewFallback(api, local)
	case api != nil:
		vec = api
	case local != nil:
		vec = local
	}
	return search.NewRetriever(search.NewPrep(d), b.Chunks, search.NewKeyword(b.Chunks), vec, search.NewErrors(b.Chunks), d.Deprecated)
}

// VectorModels describes the vector search in use, for logs: the API model
// (preferred) and the local one, as "provider/model".
func VectorModels(cfg config.Server, b index.Bundle) (api, local string) {
	if e := b.Manifest.Embedder; e != nil {
		local = e.Provider + "/" + e.Model
	}
	if e := b.Manifest.APIEmbedder; e != nil && apiVectorsWanted(cfg, *e) {
		api = e.Provider + "/" + e.Model
	}
	return api, local
}

// newLocalVector builds vector search over the manifest's primary vectors.
func newLocalVector(ctx context.Context, cfg config.Server, b index.Bundle) *search.Vector {
	e := b.Manifest.Embedder
	if e == nil {
		return nil
	}
	emb, err := newQueryEmbedder(ctx, cfg, *e)
	switch {
	case err != nil:
		slog.Warn("vector search off: embedder unavailable", "err", err)
		return nil
	case emb.Info().Dim != e.Dim:
		slog.Warn("vector search off: embedder dim mismatch", "embedder", emb.Info().Dim, "index", e.Dim)
		return nil
	}
	// cfg.VectorBackend is validated to be "memory".
	v, err := search.NewVector(emb, b.Vectors, e.Dim, b.Pieces, len(b.Chunks))
	if err != nil {
		slog.Warn("vector search off", "err", err)
		return nil
	}
	return v.SetFloor(cosineFloor(*e))
}

// newAPIVector builds vector search over the optional Cohere vectors, or
// returns nil (local vectors only) when they are absent or not enabled.
func newAPIVector(cfg config.Server, b index.Bundle) *search.Vector {
	e := b.Manifest.APIEmbedder
	if e == nil || b.APIVectors == nil {
		return nil
	}
	if !apiVectorsWanted(cfg, *e) {
		slog.Info("API vectors present but not used (needs EMBED_API=auto and EMBED_API_KEY); using local vectors",
			"api_model", e.Provider+"/"+e.Model, "embed_api", cfg.EmbedAPI)
		return nil
	}
	// The query embedder is built from the same manifest entry as the vectors,
	// so query and document vectors always come from the same model.
	emb := client.NewCohereEmbed(cfg.EmbedAPIBaseURL, cfg.EmbedAPIKey, e.Model, client.CohereSearchQuery, e.OutputDim, e.Dim)
	v, err := search.NewVector(emb, b.APIVectors, e.Dim, b.Pieces, len(b.Chunks))
	if err != nil {
		slog.Warn("API vector search off; using local vectors", "err", err)
		return nil
	}
	return v.SetFloor(cosineFloor(*e)).SetTimeout(tuning.TimeoutQueryEmbedAPI)
}

func apiVectorsWanted(cfg config.Server, e domain.EmbedInfo) bool {
	return e.Provider == "cohere" && cfg.EmbedAPI == "auto" && cfg.EmbedAPIKey != ""
}

// cosineFloor returns the calibrated floor for the model, or 0 (meaning
// tuning.FloorCosine) with a warning when it has not been calibrated.
func cosineFloor(e domain.EmbedInfo) float64 {
	key := e.Provider + "/" + e.Model
	if f, ok := tuning.FloorCosineByModel[key]; ok {
		return f
	}
	slog.Warn("no calibrated FloorCosine for this model; using the default — run make eval and add it to tuning.FloorCosineByModel",
		"model", key, "default", tuning.FloorCosine)
	return 0
}

func newQueryEmbedder(ctx context.Context, cfg config.Server, e domain.EmbedInfo) (domain.Embedder, error) {
	switch e.Provider {
	case "model2vec":
		ctx, cancel := context.WithTimeout(ctx, tuning.ModelLoadTimeout)
		defer cancel()
		return embed.New(ctx, e.Model)
	case "api":
		if cfg.EmbedBaseURL == "" || cfg.EmbedAPIKey == "" {
			return nil, fmt.Errorf("manifest uses the embedding API but EMBED_BASE_URL/EMBED_API_KEY are not set")
		}
		return client.NewEmbeddingAPI(cfg.EmbedBaseURL, cfg.EmbedAPIKey, e.Model, e.Dim), nil
	}
	return nil, fmt.Errorf("unknown embedder provider %q", e.Provider)
}

// NewLLM returns the budget-wrapped LLM, or nil when the LLM is not configured.
func NewLLM(cfg config.Server) *middleware.Budget {
	if !cfg.LLMEnabled() {
		return nil
	}
	var debug io.Writer
	if cfg.Debug {
		debug = os.Stdout
	}
	var llm domain.LLM
	switch cfg.LLMProvider {
	case "anthropic":
		c := client.NewAnthropic(cfg.LLMBaseURL, cfg.LLMAPIKey)
		c.SetDebug(debug)
		llm = c
	default:
		c := client.NewOpenAI(cfg.LLMBaseURL, cfg.LLMAPIKey)
		c.SetDebug(debug)
		llm = c
	}
	return middleware.NewBudget(llm, cfg.DailyTokenBudget)
}

// Identifiers is every identifier in the corpus plus the manifest's, in
// original and lowercased form.
func Identifiers(b index.Bundle) map[string]struct{} {
	ids := make(map[string]struct{})
	add := func(s string) {
		ids[s] = struct{}{}
		ids[strings.ToLower(s)] = struct{}{}
	}
	for _, c := range b.Chunks {
		for _, id := range utils.ExtractIdentifiers(c.Title + "\n" + c.Text) {
			add(id)
		}
	}
	for _, id := range b.Manifest.Identifiers {
		add(id)
	}
	return ids
}

// NewAnswerer wires the full answer chain (guards → cache → service). It is
// shared by the server and the evaluation runner.
func NewAnswerer(cfg config.Server, d data.Data, b index.Bundle) (domain.Answerer, *search.Retriever, error) {
	return newAnswerer(context.Background(), cfg, d, b)
}

// NewAnswererWithLLM is NewAnswerer with llm in place of the configured
// provider (no budget). The test harness (cmd/e2e) passes a recorder here
// to capture the exact LLM request without calling a provider.
func NewAnswererWithLLM(cfg config.Server, d data.Data, b index.Bundle, llm domain.LLM) (domain.Answerer, *search.Retriever) {
	r := NewRetriever(context.Background(), cfg, d, b)
	opts := answerOptions(cfg, d, b, r)
	opts.LLM = llm
	return middleware.WithGuards(middleware.WithCache(ask.NewService(opts))), r
}

func answerOptions(cfg config.Server, d data.Data, b index.Bundle, r *search.Retriever) ask.Options {
	return ask.Options{
		Retriever:   r,
		Model:       cfg.LLMModel,
		Fallback:    cfg.LLMFallbackModel,
		Rewrite:     cfg.Rewrite,
		Rerank:      cfg.Rerank,
		Data:        d,
		Identifiers: Identifiers(b),
	}
}

func newAnswerer(ctx context.Context, cfg config.Server, d data.Data, b index.Bundle) (domain.Answerer, *search.Retriever, error) {
	r := NewRetriever(ctx, cfg, d, b)
	opts := answerOptions(cfg, d, b, r)
	if budget := NewLLM(cfg); budget != nil {
		opts.LLM, opts.Budget = budget, budget
	} else {
		slog.Warn("LLM not configured (LLM_API_KEY, LLM_MODEL and base URL required): running in search-only mode")
	}
	return middleware.WithGuards(middleware.WithCache(ask.NewService(opts))), r, nil
}

// NewServer builds the HTTP server. The rate-limit sweeper stops with ctx.
func NewServer(ctx context.Context, cfg config.Server, d data.Data, b index.Bundle, logger *slog.Logger) (*http.Server, error) {
	answerer, r, err := newAnswerer(ctx, cfg, d, b)
	if err != nil {
		return nil, err
	}
	router, err := api.NewRouter(api.Options{
		Answerer: answerer,
		Health: api.HealthResponse{
			IndexVersion: b.Manifest.Version,
			Chunks:       len(b.Chunks),
			Vectors:      r.VectorsOn(),
			LLM:          cfg.LLMEnabled(),
		},
		AllowedOrigins: cfg.AllowedOrigins,
		RateLimiter:    middleware.NewRateLimiter(ctx),
		Logger:         logger,
	})
	if err != nil {
		return nil, err
	}
	vecModel, apiModel := "", ""
	if r.VectorsOn() {
		apiModel, vecModel = VectorModels(cfg, b)
	}
	llmDesc := ""
	if cfg.LLMEnabled() {
		llmDesc = cfg.LLMProvider + "/" + cfg.LLMModel
	}
	logger.Info("olake-ask starting",
		"index_version", b.Manifest.Version, "chunks", len(b.Chunks),
		"vectors", r.VectorsOn(), "vector_model", vecModel, "vector_api_model", apiModel,
		"llm", cfg.LLMEnabled(), "llm_model", llmDesc,
		"rewrite", cfg.Rewrite, "rerank", cfg.Rerank, "debug", cfg.Debug, "port", cfg.Port)
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: tuning.ReadHeaderTimeout,
		ReadTimeout:       tuning.ReadTimeout,
		WriteTimeout:      tuning.WriteTimeout,
		IdleTimeout:       tuning.IdleTimeout,
	}, nil
}
