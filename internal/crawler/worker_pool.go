package crawler

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

// WorkerPool ingests many books concurrently with a fixed number of worker goroutines.
//
//	bookIDs --> enqueueJobs --> jobs (chan) --> N workers --> results (chan) --> caller
//
// Concurrency is bounded by the number of workers, not by the number of books, so a run over
// 70,000 IDs still opens at most N simultaneous downloads.
type WorkerPool struct {
	ingester Ingester
	workers  int
}

// NewWorkerPool returns a pool of the given size around an ingester.
func NewWorkerPool(ingester Ingester, workers int) (WorkerPool, error) {
	if workers < 1 {
		return WorkerPool{}, fmt.Errorf("worker count must be a positive integer, got %d", workers)
	}
	return WorkerPool{ingester: ingester, workers: workers}, nil
}

// Run ingests every distinct book ID and streams one result per ID. Duplicate IDs are dropped
// before any work starts, so two workers never write the same book at the same time.
// Cancelling ctx stops queuing new books; the books in flight finish with a failed result.
// The channel is closed once every worker has stopped, and callers must drain it.
func (pool WorkerPool) Run(ctx context.Context, bookIDs []models.BookID) <-chan models.IngestionResult {
	jobs := make(chan models.BookID)
	results := make(chan models.IngestionResult, pool.workers)
	go enqueueJobs(ctx, distinctBookIDs(bookIDs), jobs)
	go pool.runWorkers(ctx, jobs, results)
	return results
}

// enqueueJobs feeds the jobs channel and closes it when every ID was sent or ctx is cancelled.
// The cancellation check before each send makes sure no new book starts once ctx is done.
func enqueueJobs(ctx context.Context, bookIDs []models.BookID, jobs chan<- models.BookID) {
	defer close(jobs)
	for _, bookID := range bookIDs {
		if ctx.Err() != nil {
			return
		}
		select {
		case jobs <- bookID:
		case <-ctx.Done():
			return
		}
	}
}

// runWorkers starts the workers and closes the results channel after the last one returns.
func (pool WorkerPool) runWorkers(ctx context.Context, jobs <-chan models.BookID, results chan<- models.IngestionResult) {
	var waitGroup sync.WaitGroup
	for range pool.workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			pool.work(ctx, jobs, results)
		}()
	}
	waitGroup.Wait()
	close(results)
}

// work is the loop of a single worker: take a book ID, ingest it, publish the result.
func (pool WorkerPool) work(ctx context.Context, jobs <-chan models.BookID, results chan<- models.IngestionResult) {
	for bookID := range jobs {
		results <- pool.ingester.Ingest(ctx, bookID)
	}
}

// distinctBookIDs returns the IDs sorted in ascending order without duplicates.
// The caller's slice is left untouched.
func distinctBookIDs(bookIDs []models.BookID) []models.BookID {
	distinct := slices.Clone(bookIDs)
	slices.Sort(distinct)
	return slices.Compact(distinct)
}
