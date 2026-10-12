package crawler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"boogle-engine/internal/datalake"
	"boogle-engine/internal/models"
)

// fakeFetcher serves canned books, counts calls and records the peak number of parallel calls.
type fakeFetcher struct {
	unavailable     map[models.BookID]bool
	transientFirst  int32
	calls           atomic.Int32
	inFlight        atomic.Int32
	peakConcurrency atomic.Int32
}

func (fetcher *fakeFetcher) FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error) {
	call := fetcher.calls.Add(1)
	fetcher.trackConcurrency()
	defer fetcher.inFlight.Add(-1)
	time.Sleep(5 * time.Millisecond)
	if fetcher.unavailable[bookID] {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 404)", ErrBookUnavailable, bookID)
	}
	if call <= fetcher.transientFirst {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 503)", ErrTransientDownload, bookID)
	}
	return SplitBookText(bookID, sampleText)
}

func (fetcher *fakeFetcher) trackConcurrency() {
	current := fetcher.inFlight.Add(1)
	for {
		peak := fetcher.peakConcurrency.Load()
		if current <= peak || fetcher.peakConcurrency.CompareAndSwap(peak, current) {
			return
		}
	}
}

func newTestPool(t *testing.T, fetcher *fakeFetcher, workers int) (WorkerPool, datalake.Datalake) {
	t.Helper()
	lake, err := datalake.New(t.TempDir(), datalake.DefaultBatchSize)
	if err != nil {
		t.Fatalf("create datalake: %v", err)
	}
	ingester, err := NewIngester(fetcher, lake, RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("create ingester: %v", err)
	}
	pool, err := NewWorkerPool(ingester, workers)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	return pool, lake
}

func collectByBook(results <-chan models.IngestionResult) map[models.BookID]models.IngestionResult {
	byBook := map[models.BookID]models.IngestionResult{}
	for result := range results {
		byBook[result.BookID] = result
	}
	return byBook
}

func TestWorkerPoolStoresEveryBookWithBoundedConcurrency(t *testing.T) {
	fetcher := &fakeFetcher{}
	pool, lake := newTestPool(t, fetcher, 4)
	bookIDs := make([]models.BookID, 0, 40)
	for bookID := 1; bookID <= 40; bookID++ {
		bookIDs = append(bookIDs, models.BookID(bookID))
	}
	results := collectByBook(pool.Run(context.Background(), bookIDs))
	storedIDs, err := lake.ListBookIDs()
	if err != nil {
		t.Fatalf("list datalake: %v", err)
	}
	if len(results) != 40 || len(storedIDs) != 40 {
		t.Fatalf("expected 40 results and 40 stored books, got %d and %d", len(results), len(storedIDs))
	}
	if peak := fetcher.peakConcurrency.Load(); peak > 4 || peak < 2 {
		t.Fatalf("expected between 2 and 4 parallel downloads, got %d", peak)
	}
}

func TestWorkerPoolDropsDuplicateIDsAndSkipsStoredBooks(t *testing.T) {
	fetcher := &fakeFetcher{}
	pool, _ := newTestPool(t, fetcher, 3)
	collectByBook(pool.Run(context.Background(), []models.BookID{7, 7, 7, 8}))
	second := collectByBook(pool.Run(context.Background(), []models.BookID{7, 8}))
	if calls := fetcher.calls.Load(); calls != 2 {
		t.Fatalf("expected 2 downloads in total, got %d", calls)
	}
	if second[7].Status != models.StatusAvailable || second[8].Status != models.StatusAvailable {
		t.Fatalf("expected both books to be already available, got %+v", second)
	}
}

func TestWorkerPoolClassifiesFailuresAndRetriesTransientOnes(t *testing.T) {
	fetcher := &fakeFetcher{unavailable: map[models.BookID]bool{404: true}, transientFirst: 1}
	pool, _ := newTestPool(t, fetcher, 1)
	results := collectByBook(pool.Run(context.Background(), []models.BookID{404, 1342}))
	if results[404].Status != models.StatusUnavailable {
		t.Errorf("expected book 404 to be unavailable, got %s", results[404].Status)
	}
	if results[1342].Status != models.StatusDownloaded {
		t.Errorf("expected book 1342 to be downloaded after a retry, got %s (%v)", results[1342].Status, results[1342].Err)
	}
}

func TestWorkerPoolGivesUpAfterMaxAttempts(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 100}
	pool, _ := newTestPool(t, fetcher, 1)
	results := collectByBook(pool.Run(context.Background(), []models.BookID{5}))
	if results[5].Status != models.StatusFailed || fetcher.calls.Load() != 3 {
		t.Fatalf("expected a failed result after 3 attempts, got %s after %d", results[5].Status, fetcher.calls.Load())
	}
}

func TestWorkerPoolStartsNoBookAfterCancellation(t *testing.T) {
	fetcher := &fakeFetcher{}
	pool, _ := newTestPool(t, fetcher, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := collectByBook(pool.Run(ctx, []models.BookID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}))
	if calls := fetcher.calls.Load(); calls != 0 || len(results) != 0 {
		t.Fatalf("expected no work after cancellation, got %d downloads and %d results", calls, len(results))
	}
}

func TestNewWorkerPoolRejectsZeroWorkers(t *testing.T) {
	if _, err := NewWorkerPool(Ingester{}, 0); err == nil {
		t.Fatal("expected an error for 0 workers")
	}
}
