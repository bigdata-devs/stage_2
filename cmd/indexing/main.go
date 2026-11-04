package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d ./,../../internal/api/indexingapi,../../internal/api -o ../../api/indexing -t indexing

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	_ "boogle-engine/api/indexing"
	"boogle-engine/internal/api/indexingapi"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/indexing"
	"boogle-engine/internal/mongodb"
)

const (
	defaultAddress              = ":7002"
	defaultRebuildTimeout       = 10 * time.Minute
	defaultMaxConcurrentUpdates = 8
	responseMargin              = 10 * time.Second
	shutdownGracePeriod         = 30 * time.Second
)

type serviceConfig struct {
	address              string
	datalakeDir          string
	batchSize            int
	rebuildTimeout       time.Duration
	maxConcurrentUpdates int
	pprofAddress         string
}

// @title				Boogle Engine Indexing API
// @version			1.0.0
// @description		Internal API of the Boogle Engine Indexing Service. Provider: Indexing Service.
// @description		Intended consumer: the Control Module, which orchestrates ingestion, indexing and publication.
// @description		It is not part of the front-end contract.
// @BasePath			/
// @schemes			http
// @tag.name			indexing
// @tag.description	Internal API. Provider: Indexing Service. Consumer: Control Module.
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

func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	client, err := connectDatamart()
	if err != nil {
		return err
	}
	defer closeDatamart(client)
	indexingHandler, err := buildIndexingHandler(config, client)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logStartup(config, client)
	if config.pprofAddress != "" {
		listener, err := startPprof(config.pprofAddress)
		if err != nil {
			return err
		}
		defer listener.Close()
	}
	return serve(ctx, newServer(config, newRouter(indexingHandler)))
}

func startPprof(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen pprof profiler on %s: %w", address, err)
	}
	go func() {
		if serveErr := http.Serve(listener, nil); serveErr != nil {
			slog.Error("[INDEXING] pprof profiler stopped", "error", serveErr)
		}
	}()
	slog.Info("[INDEXING] pprof profiler listening",
		"address", address,
		"profiles", "http://"+address+"/debug/pprof/",
	)
	return listener, nil
}

func connectDatamart() (mongodb.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	client, err := mongodb.Connect(ctx, mongodb.ConfigFromEnvironment(), mongodb.DefaultConnectTimeout)
	if err != nil {
		return mongodb.Client{}, fmt.Errorf("connect to datamart: %w", err)
	}
	if err := client.EnsureIndexes(ctx); err != nil {
		if closeErr := client.Close(context.Background()); closeErr != nil {
			slog.Error("[INDEXING] close datamart after failed index setup", "error", closeErr)
		}
		return mongodb.Client{}, fmt.Errorf("ensure datamart indexes: %w", err)
	}
	return client, nil
}

func closeDatamart(client mongodb.Client) {
	if err := client.Close(context.Background()); err != nil {
		slog.Error("[INDEXING] close datamart", "error", err)
	}
}

func buildIndexingHandler(config serviceConfig, client mongodb.Client) (indexingapi.IndexingHandler, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return indexingapi.IndexingHandler{}, err
	}
	service, err := newIndexingService(lake, client)
	if err != nil {
		return indexingapi.IndexingHandler{}, err
	}
	return indexingapi.NewIndexingHandler(service, config.rebuildTimeout, config.maxConcurrentUpdates)
}

func newIndexingService(lake datalake.Datalake, client mongodb.Client) (*indexing.Service, error) {
	return indexing.NewService(indexing.Ports{
		Source:  lake,
		Writer:  mongodb.NewBookIndexer(client),
		Locker:  mongodb.NewRebuildLocker(client),
		Metrics: mongodb.NewDatamartReader(client),
	})
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
	flags.DurationVar(&config.rebuildTimeout, "rebuild-timeout", defaultRebuildTimeout, "maximum duration of one full rebuild")
	flags.IntVar(&config.maxConcurrentUpdates, "max-concurrent-updates", defaultMaxConcurrentUpdates, "maximum number of POST /index/update requests indexed at the same time")
	flags.StringVar(&config.pprofAddress, "pprof", "", "host:port the net/http/pprof profiles are served on (empty disables profiling)")
	return config
}

func newRouter(indexingHandler indexingapi.IndexingHandler) *http.ServeMux {
	router := http.NewServeMux()
	indexingHandler.RegisterRoutes(router)
	router.Handle("GET /swagger/", httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json")))
	return router
}

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

func logStartup(config serviceConfig, client mongodb.Client) {
	slog.Info("[INDEXING] service listening",
		"address", config.address,
		"datalake", config.datalakeDir,
		"rebuild_timeout", config.rebuildTimeout,
		"max_concurrent_updates", config.maxConcurrentUpdates,
		"datamart", client.Describe(),
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
	slog.Info("[INDEXING] service shutting down", "grace_period", shutdownGracePeriod)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
