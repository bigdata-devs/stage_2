package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"boogle-engine/internal/control"
	"boogle-engine/internal/models"
)

// verifyIndexed reconciles the indexed books held in the control state with the
// datamart the Search Service exposes: for every book in indexed_books.txt it
// issues GET /books/{id} and reports which ones the datamart actually returns.
//
// A 404 means the datamart lost that record; any other non-2xx answer, or a
// transport error, aborts the run because the Search Service itself is not
// reachable and the reconciliation would be unreliable.
func verifyIndexed(ctx context.Context, searchURL string, perBook time.Duration, state *control.ControlState) error {
	indexed := state.IndexedIDs()
	if len(indexed) == 0 {
		slog.Info("[CONTROL] verify: nothing indexed, nothing to reconcile")
		return nil
	}
	client := &http.Client{Timeout: perBook}
	missing := []models.BookID{}
	for _, bookID := range indexed {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("verify interrupted: %w", err)
		}
		status, err := probeBook(ctx, client, searchURL, bookID)
		if err != nil {
			return fmt.Errorf("search service unreachable for book %d: %w", bookID, err)
		}
		switch status {
		case http.StatusOK:
			continue
		case http.StatusNotFound:
			slog.Warn("[CONTROL] verify: book missing from datamart", "book_id", bookID)
			missing = append(missing, bookID)
		default:
			return fmt.Errorf("search service answered %d for book %d", status, bookID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("verify: %d of %d indexed books are missing from the datamart",
			len(missing), len(indexed))
	}
	slog.Info("[CONTROL] verify: every indexed book is in the datamart",
		"verified", len(indexed))
	return nil
}

// probeBook performs one GET /books/{id} and returns the HTTP status code without
// buffering the body; the body is always drained and closed so the underlying
// connection can be reused by the next call.
func probeBook(ctx context.Context, client *http.Client, searchURL string, bookID models.BookID) (int, error) {
	url := fmt.Sprintf("%s/books/%d", searchURL, bookID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}
