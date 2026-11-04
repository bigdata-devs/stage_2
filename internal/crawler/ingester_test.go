package crawler

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"boogle-engine/internal/datalake"
	"boogle-engine/internal/models"
)

type fakeFetcher struct {
	unavailable    map[models.BookID]bool
	transientFirst int32
	calls          atomic.Int32
}

func (fetcher *fakeFetcher) FetchBook(_ context.Context, bookID models.BookID) (models.Book, error) {
	call := fetcher.calls.Add(1)
	if fetcher.unavailable[bookID] {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 404)", models.ErrBookUnavailable, bookID)
	}
	if call <= fetcher.transientFirst {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 503)", models.ErrTransient, bookID)
	}
	return SplitBookText(bookID, sampleText)
}

func newTestIngester(t *testing.T, fetcher *fakeFetcher) (Ingester, datalake.Datalake) {
	t.Helper()
	lake, err := datalake.New(t.TempDir(), datalake.DefaultBatchSize)
	if err != nil {
		t.Fatalf("create datalake: %v", err)
	}
	ingester, err := NewIngester(fetcher, lake, RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("create ingester: %v", err)
	}
	return ingester, lake
}

func TestIngesterStoresABookAndSkipsItOnceStored(t *testing.T) {
	fetcher := &fakeFetcher{}
	ingester, lake := newTestIngester(t, fetcher)

	first := ingester.Ingest(context.Background(), 7)
	second := ingester.Ingest(context.Background(), 7)

	if first.Status != models.StatusDownloaded || first.Path == "" {
		t.Fatalf("first ingestion = %+v, want downloaded with a path", first)
	}
	if second.Status != models.StatusAvailable || second.Path != first.Path {
		t.Fatalf("second ingestion = %+v, want available at %s", second, first.Path)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
	if _, err := lake.Locate(7); err != nil {
		t.Fatalf("locate the stored book: %v", err)
	}
}

func TestIngesterReportsAnUnavailableBookWithoutRetrying(t *testing.T) {
	fetcher := &fakeFetcher{unavailable: map[models.BookID]bool{404: true}}
	ingester, _ := newTestIngester(t, fetcher)

	result := ingester.Ingest(context.Background(), 404)

	if result.Status != models.StatusUnavailable || !errors.Is(result.Err, models.ErrBookUnavailable) {
		t.Fatalf("result = %+v, want unavailable wrapping ErrBookUnavailable", result)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
}

func TestIngesterRetriesATransientFailure(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 1}
	ingester, _ := newTestIngester(t, fetcher)

	result := ingester.Ingest(context.Background(), 1342)

	if result.Status != models.StatusDownloaded {
		t.Fatalf("result = %+v, want downloaded after a retry", result)
	}
	if calls := fetcher.calls.Load(); calls != 2 {
		t.Fatalf("downloads = %d, want 2", calls)
	}
}

func TestIngesterGivesUpAfterMaxAttempts(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 100}
	ingester, _ := newTestIngester(t, fetcher)

	result := ingester.Ingest(context.Background(), 5)

	if result.Status != models.StatusFailed || !errors.Is(result.Err, models.ErrTransient) {
		t.Fatalf("result = %+v, want failed wrapping ErrTransient", result)
	}
	if calls := fetcher.calls.Load(); calls != 3 {
		t.Fatalf("downloads = %d, want 3", calls)
	}
}

func TestIngesterStopsRetryingOnceTheContextEnds(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 100}
	ingester, _ := newTestIngester(t, fetcher)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := ingester.Ingest(ctx, 5)

	if result.Status != models.StatusFailed {
		t.Fatalf("result = %+v, want failed", result)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
}

func TestNewIngesterRejectsAnInvalidRetryPolicy(t *testing.T) {
	for _, policy := range []RetryPolicy{{MaxAttempts: 0}, {MaxAttempts: 1, InitialBackoff: -time.Second}} {
		if _, err := NewIngester(&fakeFetcher{}, nil, policy); err == nil {
			t.Errorf("NewIngester with %+v succeeded, want an error", policy)
		}
	}
}
