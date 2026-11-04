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
	"boogle-engine/internal/search"
)

const (
	defaultAddress      = ":7003"
	shutdownGracePeriod = 10 * time.Second
)

// @title			Boogle Engine Search API
// @version		1.0.0
// @description	Front-end-facing API of the Boogle Engine Search Service: keyword search over
// @description	Project Gutenberg books with optional author, language and publication-year filters.
// @description	Browsers may call every endpoint cross-origin (CORS) from the origins in the service's
// @description	CORS_ALLOWED_ORIGIN setting: any origin (`*`) by default, or an explicit comma-separated list.
// @description	Cross-origin pages may use GET, HEAD and OPTIONS and send the Content-Type header; credentials
// @description	(cookies, Authorization) are never allowed. Preflight OPTIONS requests are answered with 204,
// @description	or 403 `origin_not_allowed` for an origin outside the list.
// @BasePath		/
// @schemes		http
// @tag.name		search
// @tag.description	Keyword search with metadata filters and pagination
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
	searchHandler, err := buildSearchHandler(mongodb.NewSearchIndex(reader))
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

func buildSearchHandler(index search.Index) (api.SearchHandler, error) {
	service, err := search.NewService(index)
	if err != nil {
		return api.SearchHandler{}, err
	}
	return api.NewSearchHandler(service)
}

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

func newRouter(searchHandler api.SearchHandler, catalogHandler api.CatalogHandler) *http.ServeMux {
	router := http.NewServeMux()
	router.HandleFunc("GET /search", searchHandler.Search)
	catalogHandler.RegisterRoutes(router)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

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
