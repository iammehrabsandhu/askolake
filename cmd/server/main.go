// Command server serves the Ask OLake HTTP API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"olake-ask/internal/app"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/tuning"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}
	d, err := data.Load()
	if err != nil {
		return err
	}
	bundle, err := app.LoadBundle()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := app.NewServer(ctx, cfg, d, bundle, logger)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), tuning.ShutdownGrace)
	defer cancel()
	return srv.Shutdown(sctx)
}
