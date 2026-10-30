// Command ingestion is the entry point of the Boogle Engine Ingestion Service. It exposes the
// internal REST API that the Control Module calls to bring Project Gutenberg books into the
// batch-based datalake (datalake/batch_<LOW>_<HIGH>/<book_id>/), plus its OpenAPI documentation.
//
// Usage:
//
//	go run ./cmd/ingestion
//	go run ./cmd/ingestion -addr :7001 -datalake datalake -workers 8 -ingest-timeout 2m
//
// Then call it from the Control Module or by hand:
//
//	curl -X POST http://localhost:7001/ingest/1342
//	curl http://localhost:7001/ingest/status/1342
//	curl http://localhost:7001/ingest/list
//
// The interactive documentation lives at http://localhost:7001/swagger/index.html.
// Run "go run ./cmd/ingestion -h" to list every option.
//
// After changing any swag annotation, regenerate the specification in api/ingestion with:
//
//	go generate ./cmd/ingestion
package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d ./,../../internal/api -o ../../api/ingestion -t ingestion

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

	_ "boogle-engine/api/ingestion"
	"boogle-engine/internal/api"
	"boogle-engine/internal/crawler"
	"boogle-engine/internal/datalake"
)

const (
	defaultAddress       = ":7001"
	defaultWorkers       = 8
	defaultIngestTimeout = 2 * time.Minute
	// responseMargin keeps the server write deadline above the ingestion budget, so a response
	// for an ingestion that ends right at its deadline is never cut off.
	responseMargin      = 10 * time.Second
	shutdownGracePeriod = 30 * time.Second
)

// serviceConfig gathers every setting of the Ingestion Service.
type serviceConfig struct {
	address        string
	datalakeDir    string
	batchSize      int
	baseURL        string
	requestTimeout time.Duration
	retry          crawler.RetryPolicy
	limits         api.IngestionLimits
}

// main starts the Ingestion Service. The annotations below are the general information of the
// OpenAPI specification generated in api/ingestion.
//
//	@title				Boogle Engine Ingestion API
//	@version			1.0.0
//	@description		Internal API of the Boogle Engine Ingestion Service. Provider: Ingestion Service.
//	@description		Intended consumer: the Control Module, which orchestrates ingestion, indexing and publication.
//	@description		It is not part of the front-end contract.
//	@BasePath			/
//	@schemes			http
//	@tag.name			ingestion
//	@tag.description	Internal API. Provider: Ingestion Service. Consumer: Control Module.
func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("[INGESTION] service stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the service together and serves requests until Ctrl+C or SIGTERM.
func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	ingestionHandler, err := buildIngestionHandler(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logStartup(config)
	return serve(ctx, newServer(config, newRouter(ingestionHandler)))
}

// buildIngestionHandler performs the dependency injection: the Gutenberg client and the datalake
// go into the ingester, and the ingester and the datalake go into the HTTP handler.
func buildIngestionHandler(config serviceConfig) (api.IngestionHandler, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return api.IngestionHandler{}, err
	}
	client := crawler.NewGutenbergClient(config.baseURL, config.requestTimeout)
	ingester, err := crawler.NewIngester(client, lake, config.retry)
	if err != nil {
		return api.IngestionHandler{}, err
	}
	return api.NewIngestionHandler(ingester, lake, config.limits)
}

func parseConfig(arguments []string) (serviceConfig, error) {
	flags := flag.NewFlagSet("ingestion", flag.ContinueOnError)
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
	flags.StringVar(&config.baseURL, "base-url", crawler.DefaultBaseURL, "base URL of the Gutenberg mirror")
	flags.DurationVar(&config.requestTimeout, "timeout", crawler.DefaultRequestTimeout, "time limit of one download attempt")
	flags.IntVar(&config.retry.MaxAttempts, "attempts", 3, "download attempts per book on transient errors")
	flags.DurationVar(&config.retry.InitialBackoff, "backoff", time.Second, "wait before the first retry (doubles each time)")
	flags.IntVar(&config.limits.MaxConcurrentDownloads, "workers", defaultWorkers, "books downloaded at the same time")
	flags.DurationVar(&config.limits.Timeout, "ingest-timeout", defaultIngestTimeout,
		"budget of one ingestion, queueing and retries included; keep it above attempts x timeout plus backoffs")
	return config
}

// newRouter mounts the ingestion endpoints and the Swagger UI.
func newRouter(ingestionHandler api.IngestionHandler) *http.ServeMux {
	router := http.NewServeMux()
	ingestionHandler.RegisterRoutes(router)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

// newServer bounds every phase of a connection. A POST /ingest response may legitimately take as
// long as a whole ingestion, so the write deadline follows the ingestion budget.
func newServer(config serviceConfig, router http.Handler) *http.Server {
	return &http.Server{
		Addr:              config.address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      config.limits.Timeout + responseMargin,
		IdleTimeout:       60 * time.Second,
	}
}

func logStartup(config serviceConfig) {
	slog.Info("[INGESTION] service listening",
		"address", config.address,
		"datalake", config.datalakeDir,
		"workers", config.limits.MaxConcurrentDownloads,
		"ingest_timeout", config.limits.Timeout,
		"docs", "/swagger/index.html",
	)
}

// serve runs the server until ctx is cancelled, then gives in-flight requests a grace period.
// An ingestion cut short by shutdown leaves no partial book behind, because files are written
// atomically, and the caller simply retries its idempotent request.
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
	slog.Info("[INGESTION] service shutting down", "grace_period", shutdownGracePeriod)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
