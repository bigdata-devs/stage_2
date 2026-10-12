package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"boogle-engine/internal/models"
)

// BookFetcher downloads a book already split into header and body. GutenbergClient implements it.
type BookFetcher interface {
	FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error)
}

// BookStore persists split books. datalake.Datalake implements it.
type BookStore interface {
	Locate(bookID models.BookID) (models.StoredBook, error)
	Store(book models.Book) (models.StoredBook, error)
}

// RetryPolicy controls how often a transient download failure is retried inside one run.
// The wait doubles after every failed attempt, starting at InitialBackoff.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
}

// Ingester runs the whole ingestion of a single book: skip it if the datalake already holds it,
// otherwise download, split and store it. It is safe for concurrent use.
type Ingester struct {
	fetcher BookFetcher
	store   BookStore
	retry   RetryPolicy
}

// NewIngester wires a fetcher and a store together under a retry policy.
func NewIngester(fetcher BookFetcher, store BookStore, retry RetryPolicy) (Ingester, error) {
	if retry.MaxAttempts < 1 {
		return Ingester{}, fmt.Errorf("max attempts must be a positive integer, got %d", retry.MaxAttempts)
	}
	if retry.InitialBackoff < 0 {
		return Ingester{}, fmt.Errorf("initial backoff must not be negative, got %s", retry.InitialBackoff)
	}
	return Ingester{fetcher: fetcher, store: store, retry: retry}, nil
}

// Ingest brings one book into the datalake and reports the outcome. Ingesting a book that is
// already stored is a no-op, so repeated requests never duplicate work.
func (ingester Ingester) Ingest(ctx context.Context, bookID models.BookID) models.IngestionResult {
	if stored, err := ingester.store.Locate(bookID); err == nil {
		return models.IngestionResult{BookID: bookID, Status: models.StatusAvailable, Path: stored.Directory}
	}
	book, err := ingester.fetchWithRetry(ctx, bookID)
	if err != nil {
		return failedResult(bookID, err)
	}
	stored, err := ingester.store.Store(book)
	if err != nil {
		return failedResult(bookID, err)
	}
	slog.Info("[DATALAKE] book stored", "book_id", bookID, "path", stored.Directory)
	return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded, Path: stored.Directory}
}

func (ingester Ingester) fetchWithRetry(ctx context.Context, bookID models.BookID) (models.Book, error) {
	backoff := ingester.retry.InitialBackoff
	for attempt := 1; ; attempt++ {
		book, err := ingester.fetcher.FetchBook(ctx, bookID)
		if !ingester.shouldRetry(ctx, err, attempt) {
			return book, err
		}
		slog.Warn("[CRAWLER] transient failure, retrying", "book_id", bookID, "attempt", attempt, "wait", backoff, "error", err)
		if err := sleep(ctx, backoff); err != nil {
			return models.Book{}, fmt.Errorf("%w: book %d: %w", ErrTransientDownload, bookID, err)
		}
		backoff *= 2
	}
}

// shouldRetry is true only for transient failures, while attempts remain and the run is still active.
func (ingester Ingester) shouldRetry(ctx context.Context, err error, attempt int) bool {
	isTransient := errors.Is(err, ErrTransientDownload)
	return isTransient && attempt < ingester.retry.MaxAttempts && ctx.Err() == nil
}

// sleep waits for the given duration, returning early with the context error on cancellation.
func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// failedResult maps an error to a permanent (unavailable) or temporary (failed) outcome.
func failedResult(bookID models.BookID, err error) models.IngestionResult {
	status := models.StatusFailed
	if errors.Is(err, ErrBookUnavailable) {
		status = models.StatusUnavailable
	}
	return models.IngestionResult{BookID: bookID, Status: status, Err: err}
}
