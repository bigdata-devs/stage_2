package crawler

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

type WorkerPool struct {
	ingester Ingester
	workers  int
}

func NewWorkerPool(ingester Ingester, workers int) (WorkerPool, error) {
	if workers < 1 {
		return WorkerPool{}, fmt.Errorf("worker count must be a positive integer, got %d", workers)
	}
	return WorkerPool{ingester: ingester, workers: workers}, nil
}

func (pool WorkerPool) Run(ctx context.Context, bookIDs []models.BookID) <-chan models.IngestionResult {
	jobs := make(chan models.BookID)
	results := make(chan models.IngestionResult, pool.workers)
	go enqueueJobs(ctx, distinctBookIDs(bookIDs), jobs)
	go pool.runWorkers(ctx, jobs, results)
	return results
}

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

func (pool WorkerPool) work(ctx context.Context, jobs <-chan models.BookID, results chan<- models.IngestionResult) {
	for bookID := range jobs {
		results <- pool.ingester.Ingest(ctx, bookID)
	}
}

func distinctBookIDs(bookIDs []models.BookID) []models.BookID {
	distinct := slices.Clone(bookIDs)
	slices.Sort(distinct)
	return slices.Compact(distinct)
}
