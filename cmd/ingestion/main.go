// Command ingestion is the entry point of the Boogle Engine Ingestion Service. It downloads
// Project Gutenberg books with a pool of concurrent workers, splits each one into header and
// body and stores both files in the batch-based datalake: datalake/batch_<LOW>_<HIGH>/<book_id>/.
//
// Usage:
//
//	go run ./cmd/ingestion -ids 1342,84,11,174
//	go run ./cmd/ingestion -from 1 -to 500 -workers 16
//
// Run "go run ./cmd/ingestion -h" to list every option.
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

	"boogle-engine/internal/crawler"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/models"
)

const defaultWorkers = 8

// serviceConfig gathers every setting of one ingestion run.
type serviceConfig struct {
	datalakeDir    string
	batchSize      int
	baseURL        string
	requestTimeout time.Duration
	workers        int
	retry          crawler.RetryPolicy
	bookIDs        []models.BookID
}

// bookSelection holds the raw -ids, -from and -to flags before they are validated.
type bookSelection struct {
	rawIDs  string
	firstID int
	lastID  int
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("ingestion stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the service together and ingests every requested book. Ctrl+C or SIGTERM stops
// queuing new books; the books already in flight finish before the summary is logged.
func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	pool, err := buildWorkerPool(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ingestAll(ctx, pool, config.bookIDs)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run interrupted: %w", err)
	}
	return nil
}

// buildWorkerPool performs the dependency injection: the Gutenberg client and the datalake go
// into the ingester, and the ingester goes into the pool.
func buildWorkerPool(config serviceConfig) (crawler.WorkerPool, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return crawler.WorkerPool{}, err
	}
	client := crawler.NewGutenbergClient(config.baseURL, config.requestTimeout)
	ingester, err := crawler.NewIngester(client, lake, config.retry)
	if err != nil {
		return crawler.WorkerPool{}, err
	}
	return crawler.NewWorkerPool(ingester, config.workers)
}

// ingestAll runs the pool over the books and logs every result plus a final summary.
func ingestAll(ctx context.Context, pool crawler.WorkerPool, bookIDs []models.BookID) {
	startedAt := time.Now()
	summary := collectResults(pool.Run(ctx, bookIDs))
	summary.log(time.Since(startedAt))
}

func parseConfig(arguments []string) (serviceConfig, error) {
	flags := flag.NewFlagSet("ingestion", flag.ContinueOnError)
	config := registerServiceFlags(flags)
	selection := registerSelectionFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return serviceConfig{}, err
	}
	bookIDs, err := selection.bookIDs()
	if err != nil {
		return serviceConfig{}, err
	}
	config.bookIDs = bookIDs
	return *config, nil
}

func registerServiceFlags(flags *flag.FlagSet) *serviceConfig {
	config := &serviceConfig{}
	flags.StringVar(&config.datalakeDir, "datalake", "datalake", "root folder of the datalake")
	flags.IntVar(&config.batchSize, "batch-size", datalake.DefaultBatchSize, "book IDs per batch folder")
	flags.StringVar(&config.baseURL, "base-url", crawler.DefaultBaseURL, "base URL of the Gutenberg mirror")
	flags.DurationVar(&config.requestTimeout, "timeout", crawler.DefaultRequestTimeout, "time limit of one download")
	flags.IntVar(&config.workers, "workers", defaultWorkers, "number of concurrent download workers")
	flags.IntVar(&config.retry.MaxAttempts, "attempts", 3, "download attempts per book on transient errors")
	flags.DurationVar(&config.retry.InitialBackoff, "backoff", time.Second, "wait before the first retry (doubles each time)")
	return config
}

func registerSelectionFlags(flags *flag.FlagSet) *bookSelection {
	selection := &bookSelection{}
	flags.StringVar(&selection.rawIDs, "ids", "", "comma-separated book IDs, e.g. 1342,84,11")
	flags.IntVar(&selection.firstID, "from", 0, "first book ID of an inclusive range (use with -to)")
	flags.IntVar(&selection.lastID, "to", 0, "last book ID of an inclusive range (use with -from)")
	return selection
}

// bookIDs merges the -ids list and the -from/-to range; at least one of them must be given.
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
	if len(bookIDs) == 0 {
		return []models.BookID{}, errors.New("no book IDs given: use -ids and/or -from/-to")
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

// ingestionSummary counts the results of a run by status.
type ingestionSummary map[models.IngestionStatus]int

// collectResults drains the results channel, logging every book as soon as it completes.
func collectResults(results <-chan models.IngestionResult) ingestionSummary {
	summary := ingestionSummary{}
	for result := range results {
		logResult(result)
		summary[result.Status]++
	}
	return summary
}

func logResult(result models.IngestionResult) {
	if result.Err != nil {
		slog.Warn("[INGESTION] book not ingested", "book_id", result.BookID, "status", result.Status, "error", result.Err)
		return
	}
	slog.Info("[INGESTION] book ready", "book_id", result.BookID, "status", result.Status, "path", result.Path)
}

// log prints the totals and the ingestion throughput, a key metric of the Stage 2 benchmarks.
func (summary ingestionSummary) log(elapsed time.Duration) {
	downloaded := summary[models.StatusDownloaded]
	slog.Info("[INGESTION] run finished",
		"downloaded", downloaded,
		"already_available", summary[models.StatusAvailable],
		"unavailable", summary[models.StatusUnavailable],
		"failed", summary[models.StatusFailed],
		"elapsed", elapsed.Round(time.Millisecond),
		"books_per_second", fmt.Sprintf("%.2f", float64(downloaded)/elapsed.Seconds()),
	)
}
