// Command search is the entry point of the Boogle Engine Search Service. It serves the
// front-end-facing GET /search endpoint, the optional catalog endpoints GET /books/{book_id}
// and GET /search/filters, and the interactive OpenAPI documentation. Every endpoint reads the
// MongoDB datamart the Indexing Service writes: GET /search through DatamartEngine, the catalog
// endpoints through the same datamart reader.
//
// Usage:
//
//	go run ./cmd/search
//	go run ./cmd/search -addr :8080
//
// Then open http://localhost:7003/swagger/index.html or call
// http://localhost:7003/search?q=adventure&language=en
//
// Configuration comes from the environment, as in the other services:
//
//	MONGO_URI            connection string of the datamart (default mongodb://localhost:27017)
//	MONGO_DATABASE       database holding the books and inverted_index collections (default boogle)
//	CORS_ALLOWED_ORIGIN  browser origins allowed to call the API cross-origin: * for any origin
//	                     (default), or a comma-separated list such as
//	                     https://boogle.example.com,http://localhost:5173
//
// The service refuses to start when CORS_ALLOWED_ORIGIN names no valid origin or the datamart is
// unreachable (fail-fast). CORS never allows credentials: the API is public and read-only.
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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	_ "boogle-engine/api/search"
	"boogle-engine/internal/api"
	"boogle-engine/internal/mongodb"
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
//	@description	Browsers may call every endpoint cross-origin (CORS) from the origins in the service's
//	@description	CORS_ALLOWED_ORIGIN setting: any origin (`*`) by default, or an explicit comma-separated list.
//	@description	Cross-origin pages may use GET, HEAD and OPTIONS and send the Content-Type header; credentials
//	@description	(cookies, Authorization) are never allowed. Preflight OPTIONS requests are answered with 204,
//	@description	or 403 `origin_not_allowed` for an origin outside the list.
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

// run wires the service together and serves requests until Ctrl+C or SIGTERM. Configuration is
// checked before any connection is attempted. The datamart connection is then opened and fails
// fast, because no endpoint can serve without it; one stateless reader over that connection backs
// both the search engine and the catalog, and the CORS policy wraps every route.
func run(arguments []string) error {
	address, err := parseAddress(arguments)
	if err != nil {
		return err
	}
	cors, err := corsPolicyFromEnvironment()
	if err != nil {
		return err
	}
	slog.Info("cors policy", "allowed_origins", strings.Join(cors.AllowedOrigins(), ","))
	client, err := connectDatamart()
	if err != nil {
		return err
	}
	defer closeDatamart(client)
	reader := mongodb.NewDatamartReader(client)
	searchHandler, err := api.NewSearchHandler(NewDatamartEngine(reader))
	if err != nil {
		return err
	}
	catalogHandler, err := buildCatalogHandler(reader)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, newServer(address, cors.Wrap(newRouter(searchHandler, catalogHandler))))
}

// connectDatamart opens the MongoDB pool described by MONGO_URI and MONGO_DATABASE and creates
// the query indexes once; both are idempotent, so every start-up may call them.
func connectDatamart() (mongodb.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	client, err := mongodb.Connect(ctx, mongodb.ConfigFromEnvironment(), mongodb.DefaultConnectTimeout)
	if err != nil {
		return mongodb.Client{}, err
	}
	if err := client.EnsureIndexes(ctx); err != nil {
		if closeErr := client.Close(context.Background()); closeErr != nil {
			slog.Error("close datamart after failed index setup", "error", closeErr)
		}
		return mongodb.Client{}, fmt.Errorf("ensure datamart indexes: %w", err)
	}
	slog.Info("datamart connected", "datamart", client.Describe())
	return client, nil
}

// closeDatamart releases the MongoDB pool when the service shuts down.
func closeDatamart(client mongodb.Client) {
	if err := client.Close(context.Background()); err != nil {
		slog.Error("close datamart", "error", err)
	}
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
func newRouter(searchHandler api.SearchHandler, catalogHandler api.CatalogHandler) *http.ServeMux {
	router := http.NewServeMux()
	router.HandleFunc("GET /search", searchHandler.Search)
	catalogHandler.RegisterRoutes(router)
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
