package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"boogle-engine/internal/models"
)

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

type ingestionCall struct {
	done   chan struct{}
	result models.IngestionResult
}

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

func (coalescer *ingestionCoalescer) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	call := coalescer.join(bookID)
	select {
	case <-call.done:
		return call.result, nil
	case <-ctx.Done():
		return models.IngestionResult{}, ctx.Err()
	}
}

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

func (coalescer *ingestionCoalescer) run(bookID models.BookID, call *ingestionCall) {
	ctx, cancel := context.WithTimeout(context.Background(), coalescer.timeout)
	defer cancel()
	call.result = coalescer.ingestInSlot(ctx, bookID)
	coalescer.forget(bookID)
	close(call.done)
}

func (coalescer *ingestionCoalescer) ingestInSlot(ctx context.Context, bookID models.BookID) models.IngestionResult {
	select {
	case coalescer.slots <- struct{}{}:
		defer func() { <-coalescer.slots }()
		return coalescer.ingester.Ingest(ctx, bookID)
	case <-ctx.Done():
		err := fmt.Errorf("%w: book %d waited too long for a download slot: %w", models.ErrTransient, bookID, ctx.Err())
		return models.IngestionResult{BookID: bookID, Status: models.StatusFailed, Err: err}
	}
}

func (coalescer *ingestionCoalescer) forget(bookID models.BookID) {
	coalescer.mutex.Lock()
	defer coalescer.mutex.Unlock()
	delete(coalescer.inFlight, bookID)
}
