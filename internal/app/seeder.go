// Package app is the composition root: the only package that imports
// concrete implementations and wires them together.
package app

import (
	"context"
	"errors"
	"log/slog"

	"olake-ask/internal/client"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/embed"
	"olake-ask/internal/seed"
)

const crawlerUserAgent = "olake-ask-seeder/1.0"

// NewSeeder wires the seed pipeline from configuration.
func NewSeeder(cfg config.Seeder, d data.Data, outDir, workDir string) *seed.Pipeline {
	opt := seed.Options{
		Data:          d,
		Web:           client.NewWeb(crawlerUserAgent),
		GitHub:        client.NewGitHub(cfg.GitHubToken),
		Authenticated: cfg.GitHubToken != "",
		SeedIssues:    cfg.SeedIssues,
		OutDir:        outDir,
		WorkDir:       workDir,
	}
	switch cfg.Embedder {
	case "model2vec":
		opt.NewEmbedder = func(ctx context.Context) (domain.Embedder, error) {
			return embed.New(ctx, cfg.EmbedModel)
		}
	case "api":
		opt.NewEmbedder = func(context.Context) (domain.Embedder, error) {
			if cfg.EmbedBaseURL == "" || cfg.EmbedAPIKey == "" || cfg.EmbedModel == "" {
				return nil, errors.New("EMBEDDER=api requires EMBED_BASE_URL, EMBED_API_KEY and EMBED_MODEL")
			}
			return client.NewEmbeddingAPI(cfg.EmbedBaseURL, cfg.EmbedAPIKey, cfg.EmbedModel, 0), nil
		}
	}
	switch {
	case cfg.CohereEnabled():
		opt.NewAPIEmbedder = func(context.Context) (domain.Embedder, error) {
			return client.NewCohereEmbed(cfg.EmbedAPIBaseURL, cfg.EmbedAPIKey, cfg.EmbedAPIModel, client.CohereSearchDocument, cfg.EmbedAPIDim, 0), nil
		}
	case cfg.EmbedAPIModel != "" && cfg.EmbedAPI == "auto":
		slog.Warn("EMBED_API_MODEL set but Cohere vectors skipped (needs EMBEDDER=model2vec and EMBED_API_KEY)", "embedder", cfg.Embedder, "key_set", cfg.EmbedAPIKey != "")
	}
	return seed.NewPipeline(opt)
}
