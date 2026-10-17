// Command indexing is the entry point of the Boogle Engine Indexing Service. It exposes the
// internal REST API that the Control Module calls to index datalake books into an in-memory
// metadata catalog and inverted index, plus its OpenAPI documentation.
//
// Usage:
//
//	go run ./cmd/indexing
//	go run ./cmd/indexing -addr :7002 -datalake datalake -workers 8 -rebuild-timeout 10m
//
// Then call it from the Control Module or by hand:
//
//	curl -X POST http://localhost:7002/index/update/1342
//	curl -X POST http://localhost:7002/index/rebuild
//	curl http://localhost:7002/index/status
//
// The interactive documentation lives at http://localhost:7002/swagger/index.html.
// Run "go run ./cmd/indexing -h" to list every option. The index lives in memory, so it starts
// empty: call POST /index/rebuild after every start to load the books already in the datalake.
//
// After changing any swag annotation, regenerate the specification in api/indexing with:
//
//	go generate ./cmd/indexing
package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d ./,../../internal/api -o ../../api/indexing -t indexing

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	_ "boogle-engine/api/indexing"
	"boogle-engine/internal/api"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/indexer"
)

const (
	defaultAddress        = ":7002"
	defaultRebuildTimeout = 10 * time.Minute
	// responseMargin keeps the server write deadline above the rebuild budget, so the response
	// of a rebuild that ends right at its deadline is never cut off.
	responseMargin      = 10 * time.Second
	shutdownGracePeriod = 30 * time.Second
)

// serviceConfig gathers every setting of the Indexing Service.
type serviceConfig struct {
	address        string
	datalakeDir    string
	batchSize      int
	workers        int
	rebuildTimeout time.Duration
}

// main starts the Indexing Service. The annotations below are the general information of the
// OpenAPI specification generated in api/indexing.
//
//	@title				Boogle Engine Indexing API
//	@version			1.0.0
//	@description		Internal API of the Boogle Engine Indexing Service. Provider: Indexing Service.
//	@description		Intended consumer: the Control Module, which orchestrates ingestion, indexing and publication.
//	@description		It is not part of the front-end contract.
//	@BasePath			/
//	@schemes			http
//	@tag.name			indexing
//	@tag.description	Internal API. Provider: Indexing Service. Consumer: Control Module.
func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("[INDEXING] service stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the service together and serves requests until Ctrl+C or SIGTERM.
func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	indexingHandler, err := buildIndexingHandler(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logStartup(config)
	return serve(ctx, newServer(config, newRouter(indexingHandler)))
}

// buildIndexingHandler performs the dependency injection: the datalake goes into the in-memory
// published index, and the published index goes into the HTTP handler.
func buildIndexingHandler(config serviceConfig) (api.IndexingHandler, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return api.IndexingHandler{}, err
	}
	publishedIndex, err := indexer.NewPublishedIndex(lake, config.workers)
	if err != nil {
		return api.IndexingHandler{}, err
	}
	return api.NewIndexingHandler(publishedIndex, config.rebuildTimeout)
}

func parseConfig(arguments []string) (serviceConfig, error) {
	flags := flag.NewFlagSet("indexing", flag.ContinueOnError)
	config := registerFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return serviceConfig{}, err
	}
	return *config, nil
}

func registerFlags(flags *flag.FlagSet) *serviceConfig {
	config := &serviceConfig{}
	flags.StringVar(&config.address, "addr", defaultAddress, "host:port the HTTP server listens on")
	flags.StringVar(&config.datalakeDir, "datalake", "datalake", "root folder of the datalake")
	flags.IntVar(&config.batchSize, "batch-size", datalake.DefaultBatchSize, "book IDs per batch folder")
	flags.IntVar(&config.workers, "workers", runtime.NumCPU(), "goroutines that index books in parallel during a rebuild")
	flags.DurationVar(&config.rebuildTimeout, "rebuild-timeout", defaultRebuildTimeout, "maximum duration of one full rebuild")
	return config
}

// newRouter mounts the indexing endpoints and the Swagger UI.
func newRouter(indexingHandler api.IndexingHandler) *http.ServeMux {
	router := http.NewServeMux()
	indexingHandler.RegisterRoutes(router)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

// newServer bounds every phase of a connection. A POST /index/rebuild response may take as long
// as a whole rebuild, so the write deadline follows the rebuild budget.
func newServer(config serviceConfig, router http.Handler) *http.Server {
	return &http.Server{
		Addr:              config.address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      config.rebuildTimeout + responseMargin,
		IdleTimeout:       60 * time.Second,
	}
}

func logStartup(config serviceConfig) {
	slog.Info("[INDEXING] service listening",
		"address", config.address,
		"datalake", config.datalakeDir,
		"rebuild_workers", config.workers,
		"rebuild_timeout", config.rebuildTimeout,
		"docs", "/swagger/index.html",
	)
}

// serve runs the server until ctx is cancelled, then gives in-flight requests a grace period.
// A rebuild cut short by shutdown is simply discarded: nothing half-built is ever published.
func serve(ctx context.Context, server *http.Server) error {
	listenErrors := make(chan error, 1)
	go func() { listenErrors <- server.ListenAndServe() }()
	select {
	case err := <-listenErrors:
		return err
	case <-ctx.Done():
		return shutdown(server)
	}
}

func shutdown(server *http.Server) error {
	slog.Info("[INDEXING] service shutting down", "grace_period", shutdownGracePeriod)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
