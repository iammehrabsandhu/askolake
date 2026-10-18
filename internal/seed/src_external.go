package seed

import (
	"context"
	"log/slog"

	"olake-ask/internal/client"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// External fetches allowlisted external pages; failures skip the page.
func External(ctx context.Context, web *client.Web, entries []data.External) ([]domain.Chunk, error) {
	var out []domain.Chunk
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		body, status, err := web.Get(ctx, e.URL)
		if err != nil || status != 200 {
			slog.Warn("external: fetch failed", "url", e.URL, "status", status, "err", err)
			continue
		}
		page := utils.StripFragment(e.URL)
		_, sections, err := ExtractGeneric(page, body)
		if err != nil {
			slog.Warn("external: extract failed", "url", e.URL, "err", err)
			continue
		}
		src := domain.SourceExternal
		if e.Source == "docs" {
			src = domain.SourceDocs
		}
		chunks := Chunk(sections, Meta{
			PageURL:     page,
			TitlePrefix: utils.HostOf(e.URL) + ": ",
			Source:      src,
			Product:     e.Product,
		})
		if len(chunks) > tuning.ExternalMaxChunks {
			chunks = chunks[:tuning.ExternalMaxChunks]
		}
		if len(chunks) == 0 {
			slog.Warn("external: no content", "url", e.URL)
		}
		out = append(out, chunks...)
	}
	return out, nil
}
