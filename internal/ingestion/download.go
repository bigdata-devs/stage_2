package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"boogle-engine/internal/models"
)

func (service *Service) ingest(ctx context.Context, bookID models.BookID) models.IngestionResult {
	if stored, err := service.store.Locate(bookID); err == nil {
		return models.IngestionResult{BookID: bookID, Status: models.StatusAvailable, Path: stored.Directory}
	}
	select {
	case service.slots <- struct{}{}:
		defer func() { <-service.slots }()
	case <-ctx.Done():
		err := fmt.Errorf("%w: book %d waited too long for a download slot: %w", models.ErrTransient, bookID, ctx.Err())
		return models.IngestionResult{BookID: bookID, Status: models.StatusFailed, Err: err}
	}
	return service.download(ctx, bookID)
}

func (service *Service) download(ctx context.Context, bookID models.BookID) models.IngestionResult {
	book, err := service.fetchWithRetry(ctx, bookID)
	if err != nil {
		return failedResult(bookID, err)
	}
	stored, err := service.store.Store(book)
	if err != nil {
		return failedResult(bookID, err)
	}
	slog.Info("[INGESTION] book stored", "book_id", bookID, "path", stored.Directory)
	return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded, Path: stored.Directory}
}

func (service *Service) fetchWithRetry(ctx context.Context, bookID models.BookID) (models.Book, error) {
	backoff := service.retry.InitialBackoff
	for attempt := 1; ; attempt++ {
		book, err := service.fetcher.FetchBook(ctx, bookID)
		if !service.shouldRetry(ctx, err, attempt) {
			return book, err
		}
		slog.Warn("[INGESTION] transient failure, retrying", "book_id", bookID, "attempt", attempt, "wait", backoff, "error", err)
		if err := sleep(ctx, backoff); err != nil {
			return models.Book{}, fmt.Errorf("%w: book %d: %w", models.ErrTransient, bookID, err)
		}
		backoff *= 2
	}
}

func (service *Service) shouldRetry(ctx context.Context, err error, attempt int) bool {
	isTransient := errors.Is(err, models.ErrTransient)
	return isTransient && attempt < service.retry.MaxAttempts && ctx.Err() == nil
}

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

func failedResult(bookID models.BookID, err error) models.IngestionResult {
	status := models.StatusFailed
	if errors.Is(err, models.ErrBookUnavailable) {
		status = models.StatusUnavailable
	}
	return models.IngestionResult{BookID: bookID, Status: status, Err: err}
}
