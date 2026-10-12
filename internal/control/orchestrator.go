package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"boogle-engine/internal/control/httpclient"
	"boogle-engine/internal/models"
)

var (
	// ErrBookUnavailable marks a book the pipeline can never deliver, e.g. removed
	// from Project Gutenberg; repeating the step cannot succeed.
	ErrBookUnavailable = errors.New("book unavailable")
	// ErrTransientAnswer marks a service answer that may succeed if the step is
	// repeated later.
	ErrTransientAnswer = errors.New("transient service answer")
)

// IngestionService downloads books into the datalake; httpclient.IngestionClient implements it.
type IngestionService interface {
	Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error)
}

// IndexingService updates metadata and inverted index; httpclient.IndexingClient implements it.
type IndexingService interface {
	Update(ctx context.Context, bookID models.BookID) (models.IndexUpdateResult, error)
}

// RetryPolicy controls how often a failed step of one book is retried inside a run.
// The wait doubles after every transient failure, starting at InitialBackoff.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
}

// BookOutcome classifies what happened to one candidate book during a run.
type BookOutcome string

const (
	// OutcomeIndexed means the book went through the whole pipeline in this run.
	OutcomeIndexed BookOutcome = "indexed"
	// OutcomeSkipped means the book was already indexed or already failed before.
	OutcomeSkipped BookOutcome = "skipped"
	// OutcomeFailed means the book exhausted its retries or was permanently rejected.
	OutcomeFailed BookOutcome = "failed"
	// OutcomePending means the run was interrupted before the book could finish;
	// the next run picks it up again.
	OutcomePending BookOutcome = "pending"
)

// Summary counts the outcomes of one orchestrator run.
type Summary struct {
	Indexed int
	Skipped int
	Failed  int
	Pending int
}

// Total returns how many candidate books were claimed during the run.
func (summary Summary) Total() int {
	return summary.Indexed + summary.Skipped + summary.Failed + summary.Pending
}

func (summary *Summary) count(outcome BookOutcome) {
	switch outcome {
	case OutcomeIndexed:
		summary.Indexed++
	case OutcomeSkipped:
		summary.Skipped++
	case OutcomeFailed:
		summary.Failed++
	case OutcomePending:
		summary.Pending++
	}
}

// OrchestratorConfig gathers everything one orchestration run needs.
type OrchestratorConfig struct {
	Ingestion  IngestionService
	Indexing   IndexingService
	State      *ControlState
	Candidates []models.BookID
	Workers    int
	Retry      RetryPolicy
}

// Orchestrator runs the ingestion-then-indexing pipeline over a list of candidate
// books: it never indexes a book before its ingestion has completed, and it records
// every step in the ControlState so a restarted run resumes instead of duplicating
// work. Methods are safe for concurrent use.
type Orchestrator struct {
	ingestion IngestionService
	indexing  IndexingService
	state     *ControlState
	selector  *BookSelector
	workers   int
	retry     RetryPolicy
}

// NewOrchestrator wires the services, the state and the candidate list under the
// configured worker count and retry policy.
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

// Run processes every candidate book with the configured number of workers and returns
// the combined outcome counts. Cancelling ctx stops claiming new books; books already
// in flight run until their current step returns.
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
		summary.count(orchestrator.processCandidate(ctx, bookID))
	}
	return summary
}

// processCandidate skips books that need no work and runs the pipeline for the rest.
func (orchestrator Orchestrator) processCandidate(ctx context.Context, bookID models.BookID) BookOutcome {
	if orchestrator.state.IsIndexed(bookID) {
		return OutcomeSkipped
	}
	if reason, failed := orchestrator.state.FailureReason(bookID); failed {
		slog.Info("[CONTROL] skipping previously failed book", "book_id", bookID, "reason", reason)
		return OutcomeSkipped
	}
	return orchestrator.processBook(ctx, bookID)
}

func (orchestrator Orchestrator) processBook(ctx context.Context, bookID models.BookID) BookOutcome {
	slog.Info("[CONTROL] processing book", "book_id", bookID)
	if err := orchestrator.ensureDownloaded(ctx, bookID); err != nil {
		return orchestrator.finishBook(ctx, bookID, err)
	}
	if err := orchestrator.ensureIndexed(ctx, bookID); err != nil {
		return orchestrator.finishBook(ctx, bookID, err)
	}
	slog.Info("[CONTROL] book indexed", "book_id", bookID)
	return OutcomeIndexed
}

// ensureDownloaded brings a book into the datalake and records the fact. A book the
// services report as unavailable is a permanent failure; everything else is retried.
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

// ensureIndexed updates the index for a book and records the publication.
func (orchestrator Orchestrator) ensureIndexed(ctx context.Context, bookID models.BookID) error {
	if orchestrator.state.IsIndexed(bookID) {
		return nil
	}
	err := orchestrator.withRetries(ctx, "index book", func(ctx context.Context) error {
		_, updateErr := orchestrator.indexing.Update(ctx, bookID)
		return updateErr
	})
	if err != nil {
		return err
	}
	return orchestrator.state.RecordIndexed(bookID)
}

// ingestOnce performs one ingestion call and translates the answer into an error the
// retry loop can classify.
func ingestOnce(ctx context.Context, ingestion IngestionService, bookID models.BookID) error {
	result, err := ingestion.Ingest(ctx, bookID)
	if err != nil {
		return err
	}
	switch result.Status {
	case models.StatusDownloaded, models.StatusAvailable:
		return nil
	case models.StatusUnavailable:
		return fmt.Errorf("%w: service answered %q", ErrBookUnavailable, result.Status)
	default:
		return fmt.Errorf("%w: service answered %q", ErrTransientAnswer, result.Status)
	}
}

// withRetries runs step up to MaxAttempts times; permanent failures abort immediately
// and transient ones wait a doubling backoff between attempts.
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

// isPermanent reports whether repeating the same step cannot succeed.
func isPermanent(err error) bool {
	return errors.Is(err, ErrBookUnavailable) || errors.Is(err, httpclient.ErrPermanent)
}

// finishBook classifies a book that hit an error: an interrupted run leaves it pending
// for the next execution, while a real failure is recorded so it is not retried.
func (orchestrator Orchestrator) finishBook(ctx context.Context, bookID models.BookID, err error) BookOutcome {
	if ctx.Err() != nil {
		slog.Info("[CONTROL] book left pending by shutdown", "book_id", bookID, "error", err)
		return OutcomePending
	}
	slog.Warn("[CONTROL] book failed", "book_id", bookID, "error", err)
	if recordErr := orchestrator.state.RecordFailed(bookID, err.Error()); recordErr != nil {
		slog.Error("[CONTROL] could not record failure", "book_id", bookID, "error", recordErr)
	}
	return OutcomeFailed
}

func mergeSummaries(summaries <-chan Summary) Summary {
	merged := Summary{}
	for summary := range summaries {
		merged.Indexed += summary.Indexed
		merged.Skipped += summary.Skipped
		merged.Failed += summary.Failed
		merged.Pending += summary.Pending
	}
	return merged
}
