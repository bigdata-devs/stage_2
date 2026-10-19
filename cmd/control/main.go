// Command control is the entry point of the Boogle Engine Control Module: it
// orchestrates the Ingestion and Indexing Services over HTTP and records pipeline
// progress under a control directory (default "control/").
//
// Usage:
//
//	go run ./cmd/control -ids 1342,84,11 -workers 4
//	go run ./cmd/control -from 1 -to 500
//	go run ./cmd/control -discover
//
// With -discover the candidates come from GET /ingest/list, so the module processes
// every complete book of the datalake that is not indexed or failed yet.
//
// Service endpoints come from flags or the INGESTION_URL, INDEXING_URL and
// CONTROL_STATE_DIR environment variables. Run "go run ./cmd/control -h" to list
// every option.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"boogle-engine/internal/control"
	"boogle-engine/internal/control/httpclient"
	"boogle-engine/internal/models"
)

const (
	defaultIngestionURL = "http://localhost:7001"
	defaultIndexingURL  = "http://localhost:7002"
	defaultStateDir     = "control"
	defaultWorkers      = 4
	// defaultIngestTimeout stays above the Ingestion Service budget (2 minutes by default),
	// so a slow download is never aborted by the control module itself.
	defaultIngestTimeout = 150 * time.Second
	defaultTimeout       = 30 * time.Second
	defaultAttempts      = 3
	defaultBackoff       = time.Second
	readinessTimeout     = 10 * time.Second
)

// serviceConfig gathers every setting of one control run.
type serviceConfig struct {
	ingestionURL   string
	indexingURL    string
	stateDir       string
	ingestTimeout  time.Duration
	requestTimeout time.Duration
	workers        int
	maxAttempts    int
	initialBackoff time.Duration
	discover       bool
	bookIDs        []models.BookID
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("control stopped", "error", err)
		os.Exit(1)
	}
}

// run checks that both services answer, resolves the candidate books, loads the control
// state and orchestrates every requested book. Ctrl+C or SIGTERM stops claiming new books;
// the books already in flight run until their current step returns, and the summary is logged.
func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ingestion := httpclient.NewIngestionClient(config.ingestionURL, config.ingestTimeout)
	indexing := httpclient.NewIndexingClient(config.indexingURL, config.requestTimeout)
	if err := checkServices(ctx, ingestion, indexing); err != nil {
		return err
	}
	config.bookIDs, err = resolveCandidates(ctx, config, ingestion)
	if err != nil {
		return err
	}
	orchestrator, err := buildOrchestrator(config, ingestion, indexing)
	if err != nil {
		return err
	}
	slog.Info("[CONTROL] starting run",
		"books", len(config.bookIDs), "workers", config.workers, "state_dir", config.stateDir)
	startedAt := time.Now()
	summary := orchestrator.Run(ctx)
	logSummary(summary, time.Since(startedAt))
	if ctx.Err() != nil {
		return fmt.Errorf("run interrupted: %w", ctx.Err())
	}
	return nil
}

func parseConfig(arguments []string) (serviceConfig, error) {
	flags := flag.NewFlagSet("control", flag.ContinueOnError)
	config := registerServiceFlags(flags)
	selection := registerSelectionFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return serviceConfig{}, err
	}
	if config.requestTimeout <= 0 {
		return serviceConfig{}, fmt.Errorf("timeout must be positive, got %s", config.requestTimeout)
	}
	if config.ingestTimeout <= 0 {
		return serviceConfig{}, fmt.Errorf("ingest timeout must be positive, got %s", config.ingestTimeout)
	}
	bookIDs, err := selection.bookIDs()
	if err != nil {
		return serviceConfig{}, err
	}
	config.bookIDs = bookIDs
	config.discover = selection.discover
	return *config, nil
}

func registerServiceFlags(flags *flag.FlagSet) *serviceConfig {
	config := &serviceConfig{}
	flags.StringVar(&config.ingestionURL, "ingestion-url", envOr("INGESTION_URL", defaultIngestionURL),
		"base URL of the Ingestion Service (env INGESTION_URL)")
	flags.StringVar(&config.indexingURL, "indexing-url", envOr("INDEXING_URL", defaultIndexingURL),
		"base URL of the Indexing Service (env INDEXING_URL)")
	flags.StringVar(&config.stateDir, "state-dir", envOr("CONTROL_STATE_DIR", defaultStateDir),
		"folder of the control state files (env CONTROL_STATE_DIR)")
	flags.DurationVar(&config.ingestTimeout, "ingest-timeout", defaultIngestTimeout,
		"time limit of one POST /ingest call, above the service download budget")
	flags.DurationVar(&config.requestTimeout, "timeout", defaultTimeout,
		"time limit of one indexing, status or readiness call")
	flags.IntVar(&config.workers, "workers", defaultWorkers, "number of concurrent pipeline workers")
	flags.IntVar(&config.maxAttempts, "attempts", defaultAttempts, "attempts per step on transient errors")
	flags.DurationVar(&config.initialBackoff, "backoff", defaultBackoff, "wait before the first retry (doubles each time)")
	return config
}

// bookSelection holds the raw -ids, -from, -to and -discover flags before they are validated.
type bookSelection struct {
	rawIDs   string
	firstID  int
	lastID   int
	discover bool
}

func registerSelectionFlags(flags *flag.FlagSet) *bookSelection {
	selection := &bookSelection{}
	flags.StringVar(&selection.rawIDs, "ids", "", "comma-separated book IDs, e.g. 1342,84,11")
	flags.IntVar(&selection.firstID, "from", 0, "first book ID of an inclusive range (use with -to)")
	flags.IntVar(&selection.lastID, "to", 0, "last book ID of an inclusive range (use with -from)")
	flags.BoolVar(&selection.discover, "discover", false,
		"add every complete book of the datalake reported by GET /ingest/list")
	return selection
}

// bookIDs merges the -ids list and the -from/-to range; unless -discover is set, at least
// one of them must be given.
func (selection bookSelection) bookIDs() ([]models.BookID, error) {
	listedIDs, err := parseBookIDList(selection.rawIDs)
	if err != nil {
		return []models.BookID{}, err
	}
	rangeIDs, err := bookIDRange(selection.firstID, selection.lastID)
	if err != nil {
		return []models.BookID{}, err
	}
	bookIDs := append(listedIDs, rangeIDs...)
	if len(bookIDs) == 0 && !selection.discover {
		return []models.BookID{}, errors.New("no book IDs given: use -ids and/or -from/-to, or -discover")
	}
	return bookIDs, nil
}

func parseBookIDList(rawIDs string) ([]models.BookID, error) {
	bookIDs := []models.BookID{}
	for _, rawID := range strings.Split(rawIDs, ",") {
		if strings.TrimSpace(rawID) == "" {
			continue
		}
		bookID, err := models.ParseBookID(rawID)
		if err != nil {
			return []models.BookID{}, err
		}
		bookIDs = append(bookIDs, bookID)
	}
	return bookIDs, nil
}

// bookIDRange returns firstID..lastID inclusive; leaving both flags at 0 means "no range".
func bookIDRange(firstID int, lastID int) ([]models.BookID, error) {
	if firstID == 0 && lastID == 0 {
		return []models.BookID{}, nil
	}
	if firstID < 1 || lastID < firstID {
		return []models.BookID{}, fmt.Errorf("invalid range -from %d -to %d: need 1 <= from <= to", firstID, lastID)
	}
	bookIDs := make([]models.BookID, 0, lastID-firstID+1)
	for bookID := firstID; bookID <= lastID; bookID++ {
		bookIDs = append(bookIDs, models.BookID(bookID))
	}
	return bookIDs, nil
}

// checkServices fails fast when a required service is unreachable: it reads the documented
// status endpoint of each service, which answers 2xx only while the service runs. The short
// readiness deadline keeps a hanging service from blocking the start.
func checkServices(ctx context.Context, ingestion httpclient.IngestionClient, indexing httpclient.IndexingClient) error {
	readinessCtx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
	if _, err := ingestion.List(readinessCtx); err != nil {
		return fmt.Errorf("ingestion service is not ready: %w", err)
	}
	if _, err := indexing.Stats(readinessCtx); err != nil {
		return fmt.Errorf("indexing service is not ready: %w", err)
	}
	return nil
}

// resolveCandidates returns the explicitly selected books plus, in discover mode, every
// complete book of the datalake; the orchestrator skips the ones already indexed or failed.
func resolveCandidates(ctx context.Context, config serviceConfig, ingestion httpclient.IngestionClient) ([]models.BookID, error) {
	if !config.discover {
		return config.bookIDs, nil
	}
	list, err := ingestion.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover pending books: %w", err)
	}
	slog.Info("[CONTROL] discovered complete books", "count", list.Count)
	return append(config.bookIDs, list.Books...), nil
}

func buildOrchestrator(config serviceConfig, ingestion httpclient.IngestionClient, indexing httpclient.IndexingClient) (control.Orchestrator, error) {
	state, err := control.LoadState(config.stateDir)
	if err != nil {
		return control.Orchestrator{}, err
	}
	return control.NewOrchestrator(control.OrchestratorConfig{
		Ingestion:  ingestion,
		Indexing:   indexing,
		State:      state,
		Candidates: config.bookIDs,
		Workers:    config.workers,
		Retry: control.RetryPolicy{
			MaxAttempts:    config.maxAttempts,
			InitialBackoff: config.initialBackoff,
		},
	})
}

// logSummary prints the totals, the per-step coordination cost and the indexing
// throughput, the key metrics of the Stage 2 benchmarks.
func logSummary(summary control.Summary, elapsed time.Duration) {
	pipelineBooks := summary.Indexed + summary.Failed
	slog.Info("[CONTROL] run finished",
		"indexed", summary.Indexed,
		"skipped", summary.Skipped,
		"failed", summary.Failed,
		"pending", summary.Pending,
		"elapsed", elapsed.Round(time.Millisecond),
		"books_per_second", fmt.Sprintf("%.2f", float64(summary.Indexed)/elapsed.Seconds()),
		"ingest_step_total", summary.IngestElapsed.Round(time.Millisecond),
		"ingest_step_avg", averageStepDuration(summary.IngestElapsed, pipelineBooks),
		"index_step_total", summary.IndexElapsed.Round(time.Millisecond),
		"index_step_avg", averageStepDuration(summary.IndexElapsed, pipelineBooks),
	)
}

// averageStepDuration spreads one step total over the books that entered the pipeline.
func averageStepDuration(total time.Duration, books int) time.Duration {
	if books == 0 {
		return 0
	}
	return (total / time.Duration(books)).Round(time.Millisecond)
}

// envOr reads an environment variable or falls back to a default value.
func envOr(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
