package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d ./,../../internal/api/ingestionapi,../../internal/api -o ../../api/ingestion -t ingestion

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
	"boogle-engine/internal/api/ingestionapi"
	"boogle-engine/internal/crawler"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/ingestion"
)

const (
	defaultAddress       = ":7001"
	defaultWorkers       = 8
	defaultIngestTimeout = 2 * time.Minute
	responseMargin       = 10 * time.Second
	shutdownGracePeriod  = 30 * time.Second
)

type serviceConfig struct {
	address        string
	datalakeDir    string
	batchSize      int
	baseURL        string
	requestTimeout time.Duration
	retry          ingestion.RetryPolicy
	limits         ingestion.Limits
}

// @title				Boogle Engine Ingestion API
// @version			1.0.0
// @description		Internal API of the Boogle Engine Ingestion Service. Provider: Ingestion Service.
// @description		Intended consumer: the Control Module, which orchestrates ingestion, indexing and publication.
// @description		It is not part of the front-end contract.
// @BasePath			/
// @schemes			http
// @tag.name			ingestion
// @tag.description	Internal API. Provider: Ingestion Service. Consumer: Control Module.
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

func buildIngestionHandler(config serviceConfig) (ingestionapi.IngestionHandler, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return ingestionapi.IngestionHandler{}, err
	}
	client := crawler.NewGutenbergClient(config.baseURL, config.requestTimeout)
	service, err := ingestion.NewService(ingestion.Ports{Fetcher: client, Store: lake}, config.retry, config.limits)
	if err != nil {
		return ingestionapi.IngestionHandler{}, err
	}
	return ingestionapi.NewIngestionHandler(service, service)
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

func newRouter(ingestionHandler ingestionapi.IngestionHandler) *http.ServeMux {
	router := http.NewServeMux()
	ingestionHandler.RegisterRoutes(router)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

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
