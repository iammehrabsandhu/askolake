// Command seeder collects OLake content and writes the index bundle.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"

	"olake-ask/internal/app"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
)

func main() {
	out := flag.String("out", "assets/index", "output bundle directory")
	work := flag.String("work", ".work", "working directory for repository checkouts")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := run(*out, *work); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

func run(out, work string) error {
	cfg, err := config.LoadSeeder()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	d, err := data.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sum, err := app.NewSeeder(cfg, d, out, work).Run(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nSOURCE\tCHUNKS")
	for _, s := range domain.SourceOrder {
		fmt.Fprintf(tw, "%s\t%d\n", s, sum.Counts[s])
	}
	fmt.Fprintf(tw, "total\t%d\n", sum.Chunks)
	fmt.Fprintf(tw, "pieces\t%d\n", sum.Pieces)
	if sum.Embedder != nil {
		fmt.Fprintf(tw, "embedder\t%s/%s (dim %d)\n", sum.Embedder.Provider, sum.Embedder.Model, sum.Embedder.Dim)
	} else {
		fmt.Fprintf(tw, "embedder\tnone\n")
	}
	if sum.APIEmbedder != nil {
		fmt.Fprintf(tw, "api embedder\t%s/%s (dim %d)\n", sum.APIEmbedder.Provider, sum.APIEmbedder.Model, sum.APIEmbedder.Dim)
	}
	names := make([]string, 0, len(sum.Repos))
	for name := range sum.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(tw, "repo %s\t%s\n", name, sum.Repos[name])
	}
	fmt.Fprintf(tw, "version\t%s\n", sum.Version)
	fmt.Fprintf(tw, "duration\t%s\n", sum.Duration.Round(1e8))
	fmt.Fprintf(tw, "output\t%s\n", sum.OutDir)
	fmt.Fprintf(tw, "warnings\t%d\n", len(sum.Warnings))
	return tw.Flush()
}
