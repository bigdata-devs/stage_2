package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"boogle-engine/internal/models"
)

type IngestionService interface {
	Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error)
}

type IndexingService interface {
	Update(ctx context.Context, bookID models.BookID) error
}

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
}

type BookOutcome string

const (
	OutcomeIndexed BookOutcome = "indexed"
	OutcomeSkipped BookOutcome = "skipped"
	OutcomeFailed  BookOutcome = "failed"
	OutcomePending BookOutcome = "pending"
)

type Summary struct {
	Indexed int
	Skipped int
	Failed  int
	Pending int
	// IngestElapsed totals the ingestion steps that ran during this run.
	IngestElapsed time.Duration
	// IndexElapsed totals the index-update steps that ran during this run.
	IndexElapsed time.Duration
}

func (summary Summary) Total() int {
	return summary.Indexed + summary.Skipped + summary.Failed + summary.Pending
}

type bookReport struct {
	outcome       BookOutcome
	ingestElapsed time.Duration
	indexElapsed  time.Duration
}

func (summary *Summary) add(report bookReport) {
	switch report.outcome {
	case OutcomeIndexed:
		summary.Indexed++
	case OutcomeSkipped:
		summary.Skipped++
	case OutcomeFailed:
		summary.Failed++
	case OutcomePending:
		summary.Pending++
	}
	summary.IngestElapsed += report.ingestElapsed
	summary.IndexElapsed += report.indexElapsed
}

type OrchestratorConfig struct {
	Ingestion  IngestionService
	Indexing   IndexingService
	State      *ControlState
	Candidates []models.BookID
	Workers    int
	Retry      RetryPolicy
}

type Orchestrator struct {
	ingestion IngestionService
	indexing  IndexingService
	state     *ControlState
	selector  *BookSelector
	workers   int
	retry     RetryPolicy
}

func NewOrchestrator(config OrchestratorConfig) (Orchestrator, error) {
	if config.Workers < 1 {
		return Orchestrator{}, fmt.Errorf("workers must be a positive integer, got %d", config.Workers)
	}
	if config.Retry.MaxAttempts < 1 {
		return Orchestrator{}, fmt.Errorf("max attempts must be a positive integer, got %d", config.Retry.MaxAttempts)
	}
	if config.Retry.InitialBackoff < 0 {
		return Orchestrator{}, fmt.Errorf("initial backoff must not be negative, got %s", config.Retry.InitialBackoff)
	}
	selector := NewBookSelector(config.Candidates)
	return Orchestrator{
		ingestion: config.Ingestion,
		indexing:  config.Indexing,
		state:     config.State,
		selector:  &selector,
		workers:   config.Workers,
		retry:     config.Retry,
	}, nil
}

func (orchestrator Orchestrator) Run(ctx context.Context) Summary {
	summaries := make(chan Summary, orchestrator.workers)
	var group sync.WaitGroup
	for range orchestrator.workers {
		group.Add(1)
		go func() {
			defer group.Done()
			summaries <- orchestrator.runWorker(ctx)
		}()
	}
	group.Wait()
	close(summaries)
	return mergeSummaries(summaries)
}

func (orchestrator Orchestrator) runWorker(ctx context.Context) Summary {
	summary := Summary{}
	for ctx.Err() == nil {
		bookID, claimed := orchestrator.selector.Claim()
		if !claimed {
			break
		}
		summary.add(orchestrator.processCandidate(ctx, bookID))
	}
	return summary
}

func (orchestrator Orchestrator) processCandidate(ctx context.Context, bookID models.BookID) bookReport {
	if orchestrator.state.IsIndexed(bookID) {
		return bookReport{outcome: OutcomeSkipped}
	}
	if reason, failed := orchestrator.state.FailureReason(bookID); failed {
		slog.Info("[CONTROL] skipping previously failed book", "book_id", bookID, "reason", reason)
		return bookReport{outcome: OutcomeSkipped}
	}
	return orchestrator.processBook(ctx, bookID)
}

func (orchestrator Orchestrator) processBook(ctx context.Context, bookID models.BookID) bookReport {
	slog.Info("[CONTROL] processing book", "book_id", bookID)
	ingestStart := time.Now()
	if err := orchestrator.ensureDownloaded(ctx, bookID); err != nil {
		return orchestrator.finishBook(ctx, bookID, err)
	}
	ingestElapsed := time.Since(ingestStart)
	slog.Info("[CONTROL] ingest step finished", "book_id", bookID, "duration", ingestElapsed)
	indexStart := time.Now()
	if err := orchestrator.ensureIndexed(ctx, bookID); err != nil {
		report := orchestrator.finishBook(ctx, bookID, err)
		report.ingestElapsed = ingestElapsed
		return report
	}
	indexElapsed := time.Since(indexStart)
	slog.Info("[CONTROL] index step finished", "book_id", bookID, "duration", indexElapsed)
	slog.Info("[CONTROL] book indexed", "book_id", bookID)
	return bookReport{outcome: OutcomeIndexed, ingestElapsed: ingestElapsed, indexElapsed: indexElapsed}
}

func (orchestrator Orchestrator) ensureDownloaded(ctx context.Context, bookID models.BookID) error {
	if orchestrator.state.IsDownloaded(bookID) {
		return nil
	}
	err := orchestrator.withRetries(ctx, "ingest book", func(ctx context.Context) error {
		return ingestOnce(ctx, orchestrator.ingestion, bookID)
	})
	if err != nil {
		return err
	}
	return orchestrator.state.RecordDownloaded(bookID)
}

func (orchestrator Orchestrator) ensureIndexed(ctx context.Context, bookID models.BookID) error {
	if orchestrator.state.IsIndexed(bookID) {
		return nil
	}
	err := orchestrator.withRetries(ctx, "index book", func(ctx context.Context) error {
		return orchestrator.indexing.Update(ctx, bookID)
	})
	if err != nil {
		return err
	}
	return orchestrator.state.RecordIndexed(bookID)
}

func ingestOnce(ctx context.Context, ingestion IngestionService, bookID models.BookID) error {
	result, err := ingestion.Ingest(ctx, bookID)
	if err != nil {
		return err
	}
	switch result.Status {
	case models.StatusDownloaded, models.StatusAvailable:
		return nil
	case models.StatusUnavailable:
		return fmt.Errorf("%w: service answered %q", models.ErrBookUnavailable, result.Status)
	default:
		return fmt.Errorf("%w: service answered %q", models.ErrTransient, result.Status)
	}
}

func (orchestrator Orchestrator) withRetries(ctx context.Context, step string, runStep func(context.Context) error) error {
	backoff := orchestrator.retry.InitialBackoff
	var err error
	for attempt := 1; attempt <= orchestrator.retry.MaxAttempts; attempt++ {
		err = runStep(ctx)
		if err == nil {
			return nil
		}
		if isPermanent(err) {
			return err
		}
		if attempt == orchestrator.retry.MaxAttempts {
			break
		}
		slog.Warn("[CONTROL] transient failure, retrying",
			"step", step, "attempt", attempt, "backoff", backoff, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s interrupted: %w", step, ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return fmt.Errorf("%s failed after %d attempts: %w", step, orchestrator.retry.MaxAttempts, err)
}

func isPermanent(err error) bool {
	return errors.Is(err, models.ErrBookUnavailable) || errors.Is(err, models.ErrPermanent)
}

func (orchestrator Orchestrator) finishBook(ctx context.Context, bookID models.BookID, err error) bookReport {
	if ctx.Err() != nil {
		slog.Info("[CONTROL] book left pending by shutdown", "book_id", bookID, "error", err)
		return bookReport{outcome: OutcomePending}
	}
	slog.Warn("[CONTROL] book failed", "book_id", bookID, "error", err)
	if recordErr := orchestrator.state.RecordFailed(bookID, err.Error()); recordErr != nil {
		slog.Error("[CONTROL] could not record failure", "book_id", bookID, "error", recordErr)
	}
	return bookReport{outcome: OutcomeFailed}
}

func mergeSummaries(summaries <-chan Summary) Summary {
	merged := Summary{}
	for summary := range summaries {
		merged.Indexed += summary.Indexed
		merged.Skipped += summary.Skipped
		merged.Failed += summary.Failed
		merged.Pending += summary.Pending
		merged.IngestElapsed += summary.IngestElapsed
		merged.IndexElapsed += summary.IndexElapsed
	}
	return merged
}
