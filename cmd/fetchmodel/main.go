// Command fetchmodel pre-downloads the embedding model named in the embedded
// index manifest (Docker build step), so the server starts without network.
package main

import (
	"context"
	"fmt"
	"os"

	"olake-ask/assets"
	"olake-ask/internal/embed"
	"olake-ask/internal/index"
	"olake-ask/internal/tuning"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fetchmodel:", err)
		os.Exit(1)
	}
}

func run() error {
	fsys, err := assets.Index()
	if err != nil {
		return err
	}
	m, err := index.ReadManifest(fsys)
	if err != nil {
		return err
	}
	if m.Embedder == nil || m.Embedder.Provider != "model2vec" {
		fmt.Println("no local model required")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), tuning.ModelLoadTimeout)
	defer cancel()
	e, err := embed.New(ctx, m.Embedder.Model)
	if err != nil {
		return err
	}
	fmt.Printf("model %s ready (dim %d)\n", m.Embedder.Model, e.Info().Dim)
	return e.Close()
}
