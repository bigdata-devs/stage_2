// Command search is the entry point of the Boogle Engine Search Service. It serves the
// front-end-facing GET /search endpoint and its interactive OpenAPI documentation. Until the
// MongoDB datamart is ready, searches are answered by api.StubEngine's sample catalog.
//
// Usage:
//
//	go run ./cmd/search
//	go run ./cmd/search -addr :8080
//
// Then open http://localhost:7003/swagger/index.html or call
// http://localhost:7003/search?q=adventure&language=en
//
// After changing any swag annotation, regenerate the specification in api/search with:
//
//	go generate ./cmd/search
package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d ./,../../internal/api -o ../../api/search -t search

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	_ "boogle-engine/api/search"
	"boogle-engine/internal/api"
)

const (
	defaultAddress      = ":7003"
	shutdownGracePeriod = 10 * time.Second
)

// main starts the Search Service. The annotations below are the general information of the
// OpenAPI specification generated in api/search.
//
//	@title			Boogle Engine Search API
//	@version		1.0.0
//	@description	Front-end-facing API of the Boogle Engine Search Service: keyword search over
//	@description	Project Gutenberg books with optional author, language and publication-year filters.
//	@BasePath		/
//	@schemes		http
//	@tag.name		search
//	@tag.description	Keyword search with metadata filters and pagination
func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("search service stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the service together and serves requests until Ctrl+C or SIGTERM.
func run(arguments []string) error {
	address, err := parseAddress(arguments)
	if err != nil {
		return err
	}
	searchHandler, err := api.NewSearchHandler(api.NewStubEngine())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, newServer(address, newRouter(searchHandler)))
}

func parseAddress(arguments []string) (string, error) {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	address := flags.String("addr", defaultAddress, "host:port the HTTP server listens on")
	if err := flags.Parse(arguments); err != nil {
		return "", err
	}
	return *address, nil
}

// newRouter maps every route of the service using the Go 1.22 "METHOD /path" patterns, so
// other methods on /search are answered with 405 Method Not Allowed.
func newRouter(searchHandler api.SearchHandler) *http.ServeMux {
	router := http.NewServeMux()
	router.HandleFunc("GET /search", searchHandler.Search)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

// newServer bounds every phase of a connection so slow clients cannot pin goroutines forever.
func newServer(address string, router http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// serve runs the server until ctx is cancelled, then lets in-flight requests finish.
func serve(ctx context.Context, server *http.Server) error {
	listenErrors := make(chan error, 1)
	go func() { listenErrors <- server.ListenAndServe() }()
	slog.Info("search service listening", "address", server.Addr, "docs", "/swagger/index.html")
	select {
	case err := <-listenErrors:
		return err
	case <-ctx.Done():
		return shutdown(server)
	}
}

func shutdown(server *http.Server) error {
	slog.Info("search service shutting down", "grace_period", shutdownGracePeriod)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
