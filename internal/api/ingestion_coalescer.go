package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"boogle-engine/internal/crawler"
	"boogle-engine/internal/models"
)

// IngestionLimits bounds the work that the Ingestion API starts on behalf of its callers.
type IngestionLimits struct {
	// Timeout is the budget of one ingestion: waiting for a download slot plus every download attempt.
	Timeout time.Duration
	// MaxConcurrentDownloads is how many different books may be ingested at the same time.
	MaxConcurrentDownloads int
}

func (limits IngestionLimits) validate() error {
	if limits.Timeout <= 0 {
		return fmt.Errorf("ingestion timeout must be positive, got %s", limits.Timeout)
	}
	if limits.MaxConcurrentDownloads < 1 {
		return fmt.Errorf("max concurrent downloads must be a positive integer, got %d", limits.MaxConcurrentDownloads)
	}
	return nil
}

// ingestionCall is one ingestion in progress. result is written once, before done is closed.
type ingestionCall struct {
	done   chan struct{}
	result models.IngestionResult
}

// ingestionCoalescer runs the ingestions that HTTP requests ask for:
//
//   - concurrent requests for the same book join one shared ingestion (single flight), so this
//     process never downloads a book twice at the same time;
//   - at most MaxConcurrentDownloads books are ingested at once and the others wait for a slot;
//   - each ingestion runs detached from the requests waiting for it, under its own timeout, so a
//     caller that disconnects cancels neither the work of the other callers nor its own retry.
//
// It is safe for concurrent use. Its lock only coordinates the goroutines of one process, not
// separate service replicas.
type ingestionCoalescer struct {
	ingester BookIngester
	timeout  time.Duration
	slots    chan struct{}
	mutex    sync.Mutex
	inFlight map[models.BookID]*ingestionCall
}

func newIngestionCoalescer(ingester BookIngester, limits IngestionLimits) *ingestionCoalescer {
	return &ingestionCoalescer{
		ingester: ingester,
		timeout:  limits.Timeout,
		slots:    make(chan struct{}, limits.MaxConcurrentDownloads),
		inFlight: map[models.BookID]*ingestionCall{},
	}
}

// Ingest waits for the shared ingestion of a book, starting it when none is running. It returns
// ctx.Err() if the caller stops waiting first; the ingestion itself carries on.
func (coalescer *ingestionCoalescer) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	call := coalescer.join(bookID)
	select {
	case <-call.done:
		return call.result, nil
	case <-ctx.Done():
		return models.IngestionResult{}, ctx.Err()
	}
}

// join returns the ingestion in progress for the book, starting a new one when there is none.
func (coalescer *ingestionCoalescer) join(bookID models.BookID) *ingestionCall {
	coalescer.mutex.Lock()
	defer coalescer.mutex.Unlock()
	if call, running := coalescer.inFlight[bookID]; running {
		return call
	}
	call := &ingestionCall{done: make(chan struct{})}
	coalescer.inFlight[bookID] = call
	go coalescer.run(bookID, call)
	return call
}

// run performs one shared ingestion and then publishes its result to every waiting request.
func (coalescer *ingestionCoalescer) run(bookID models.BookID, call *ingestionCall) {
	ctx, cancel := context.WithTimeout(context.Background(), coalescer.timeout)
	defer cancel()
	call.result = coalescer.ingestInSlot(ctx, bookID)
	coalescer.forget(bookID)
	close(call.done)
}

// ingestInSlot waits for a free download slot and ingests the book while holding it. Running out
// of time while queued is reported as a transient failure, because a later retry may succeed.
func (coalescer *ingestionCoalescer) ingestInSlot(ctx context.Context, bookID models.BookID) models.IngestionResult {
	select {
	case coalescer.slots <- struct{}{}:
		defer func() { <-coalescer.slots }()
		return coalescer.ingester.Ingest(ctx, bookID)
	case <-ctx.Done():
		err := fmt.Errorf("%w: book %d waited too long for a download slot: %w", crawler.ErrTransientDownload, bookID, ctx.Err())
		return models.IngestionResult{BookID: bookID, Status: models.StatusFailed, Err: err}
	}
}

func (coalescer *ingestionCoalescer) forget(bookID models.BookID) {
	coalescer.mutex.Lock()
	defer coalescer.mutex.Unlock()
	delete(coalescer.inFlight, bookID)
}
