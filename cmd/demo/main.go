// Command demo serves web/ locally to try the widget against a running API.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"olake-ask/internal/tuning"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	api := flag.String("api", "http://localhost:8080", "API URL shown in the printed demo link")
	flag.Parse()

	port := *addr
	if len(port) > 0 && port[0] == ':' {
		port = "localhost" + port
	}
	fmt.Printf("Open http://%s/?api=%s\n", port, url.QueryEscape(*api))
	srv := &http.Server{
		Addr:              *addr,
		Handler:           http.FileServer(http.Dir("web")),
		ReadHeaderTimeout: tuning.ReadHeaderTimeout,
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("demo server failed", "err", err)
		os.Exit(1)
	}
}
