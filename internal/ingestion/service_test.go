package ingestion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boogle-engine/internal/models"
)

var (
	testRetry  = RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}
	testLimits = Limits{Timeout: 5 * time.Second, MaxConcurrentDownloads: 4}
)

type fakeFetcher struct {
	unavailable    map[models.BookID]bool
	transientFirst int32
	release        chan struct{}
	calls          atomic.Int32
}

func (fetcher *fakeFetcher) FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error) {
	call := fetcher.calls.Add(1)
	if fetcher.release != nil {
		select {
		case <-fetcher.release:
		case <-ctx.Done():
			return models.Book{}, fmt.Errorf("%w: %w", models.ErrTransient, ctx.Err())
		}
	}
	if fetcher.unavailable[bookID] {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 404)", models.ErrBookUnavailable, bookID)
	}
	if call <= fetcher.transientFirst {
		return models.Book{}, fmt.Errorf("%w: book %d (HTTP 503)", models.ErrTransient, bookID)
	}
	return models.Book{ID: bookID, Header: "Title: Book " + bookID.String(), Body: "Text of book " + bookID.String()}, nil
}

type memoryStore struct {
	mutex    sync.Mutex
	books    map[models.BookID]models.StoredBook
	storeErr error
}

func newMemoryStore(bookIDs ...models.BookID) *memoryStore {
	store := &memoryStore{books: map[models.BookID]models.StoredBook{}}
	for _, bookID := range bookIDs {
		store.books[bookID] = storedBook(bookID)
	}
	return store
}

func storedBook(bookID models.BookID) models.StoredBook {
	return models.StoredBook{ID: bookID, Directory: "datalake/" + bookID.String()}
}

func (store *memoryStore) Locate(bookID models.BookID) (models.StoredBook, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	stored, found := store.books[bookID]
	if !found {
		return models.StoredBook{}, fmt.Errorf("%w: book %d", models.ErrBookNotFound, bookID)
	}
	return stored, nil
}

func (store *memoryStore) Store(book models.Book) (models.StoredBook, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.storeErr != nil {
		return models.StoredBook{}, store.storeErr
	}
	stored := storedBook(book.ID)
	store.books[book.ID] = stored
	return stored, nil
}

func (store *memoryStore) ListBookIDs() ([]models.BookID, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	bookIDs := make([]models.BookID, 0, len(store.books))
	for bookID := range store.books {
		bookIDs = append(bookIDs, bookID)
	}
	slices.Sort(bookIDs)
	return bookIDs, nil
}

func newTestService(t *testing.T, fetcher *fakeFetcher, store *memoryStore, retry RetryPolicy, limits Limits) *Service {
	t.Helper()
	service, err := NewService(Ports{Fetcher: fetcher, Store: store}, retry, limits)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func ingest(t *testing.T, service *Service, bookID models.BookID) models.IngestionResult {
	t.Helper()
	result, err := service.Ingest(context.Background(), bookID)
	if err != nil {
		t.Fatalf("Ingest(%d): %v", bookID, err)
	}
	return result
}

func TestNewServiceRejectsAnInvalidConfiguration(t *testing.T) {
	ports := Ports{Fetcher: &fakeFetcher{}, Store: newMemoryStore()}
	cases := []struct {
		name   string
		ports  Ports
		retry  RetryPolicy
		limits Limits
	}{
		{name: "missing fetcher", ports: Ports{Store: newMemoryStore()}, retry: testRetry, limits: testLimits},
		{name: "missing store", ports: Ports{Fetcher: &fakeFetcher{}}, retry: testRetry, limits: testLimits},
		{name: "no attempts", ports: ports, retry: RetryPolicy{}, limits: testLimits},
		{name: "negative backoff", ports: ports, retry: RetryPolicy{MaxAttempts: 1, InitialBackoff: -time.Second}, limits: testLimits},
		{name: "no timeout", ports: ports, retry: testRetry, limits: Limits{MaxConcurrentDownloads: 1}},
		{name: "no download slot", ports: ports, retry: testRetry, limits: Limits{Timeout: time.Second}},
	}
	for _, testCase := range cases {
		if _, err := NewService(testCase.ports, testCase.retry, testCase.limits); err == nil {
			t.Errorf("%s: NewService succeeded, want an error", testCase.name)
		}
	}
	if _, err := NewService(Ports{}, testRetry, testLimits); !errors.Is(err, ErrMissingPort) {
		t.Errorf("NewService without ports = %v, want ErrMissingPort", err)
	}
}

func TestIngestStoresABookAndAnswersAvailableOnceStored(t *testing.T) {
	fetcher := &fakeFetcher{}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)

	first := ingest(t, service, 7)
	second := ingest(t, service, 7)

	if first.Status != models.StatusDownloaded || first.Path != "datalake/7" {
		t.Fatalf("first ingestion = %+v, want downloaded at datalake/7", first)
	}
	if second.Status != models.StatusAvailable || second.Path != first.Path {
		t.Fatalf("second ingestion = %+v, want available at %s", second, first.Path)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
}

func TestIngestAnswersAStoredBookWithoutWaitingForADownloadSlot(t *testing.T) {
	fetcher := &fakeFetcher{}
	service := newTestService(t, fetcher, newMemoryStore(1342), testRetry, Limits{Timeout: 5 * time.Second, MaxConcurrentDownloads: 1})
	service.slots <- struct{}{}

	result := ingest(t, service, 1342)

	if result.Status != models.StatusAvailable || result.Path != "datalake/1342" {
		t.Fatalf("result = %+v, want available at datalake/1342", result)
	}
	if calls := fetcher.calls.Load(); calls != 0 {
		t.Fatalf("downloads = %d, want 0", calls)
	}
}

func TestIngestReportsAnUnavailableBookWithoutRetrying(t *testing.T) {
	fetcher := &fakeFetcher{unavailable: map[models.BookID]bool{404: true}}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)

	result := ingest(t, service, 404)

	if result.Status != models.StatusUnavailable || !errors.Is(result.Err, models.ErrBookUnavailable) {
		t.Fatalf("result = %+v, want unavailable wrapping ErrBookUnavailable", result)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
}

func TestIngestRetriesATransientFailure(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 1}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)

	result := ingest(t, service, 1342)

	if result.Status != models.StatusDownloaded {
		t.Fatalf("result = %+v, want downloaded after a retry", result)
	}
	if calls := fetcher.calls.Load(); calls != 2 {
		t.Fatalf("downloads = %d, want 2", calls)
	}
}

func TestIngestGivesUpAfterMaxAttempts(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 100}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)

	result := ingest(t, service, 5)

	if result.Status != models.StatusFailed || !errors.Is(result.Err, models.ErrTransient) {
		t.Fatalf("result = %+v, want failed wrapping ErrTransient", result)
	}
	if calls := fetcher.calls.Load(); calls != 3 {
		t.Fatalf("downloads = %d, want 3", calls)
	}
}

func TestIngestStopsRetryingWhenTheBudgetRunsOut(t *testing.T) {
	fetcher := &fakeFetcher{transientFirst: 100}
	retry := RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Hour}
	service := newTestService(t, fetcher, newMemoryStore(), retry, Limits{Timeout: 50 * time.Millisecond, MaxConcurrentDownloads: 1})

	result := ingest(t, service, 5)

	if result.Status != models.StatusFailed || !errors.Is(result.Err, models.ErrTransient) {
		t.Fatalf("result = %+v, want failed wrapping ErrTransient", result)
	}
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
}

func TestIngestReportsAStorageFailureAsNonTransient(t *testing.T) {
	store := newMemoryStore()
	store.storeErr = errors.New("disk full")
	service := newTestService(t, &fakeFetcher{}, store, testRetry, testLimits)

	result := ingest(t, service, 1342)

	if result.Status != models.StatusFailed || !errors.Is(result.Err, store.storeErr) || errors.Is(result.Err, models.ErrTransient) {
		t.Fatalf("result = %+v, want failed with the storage error only", result)
	}
}

func TestConcurrentIngestionsOfTheSameBookShareOneDownload(t *testing.T) {
	fetcher := &fakeFetcher{release: make(chan struct{})}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)

	first, second, otherBook := service.join(1342), service.join(1342), service.join(84)
	close(fetcher.release)
	<-first.done
	<-otherBook.done

	if first != second || first == otherBook {
		t.Errorf("the same book must share one ingestion and different books must not")
	}
	if calls := fetcher.calls.Load(); calls != 2 {
		t.Errorf("downloads = %d, want 2 (one per distinct book)", calls)
	}
}

func TestIngestReportsATransientFailureWhenNoSlotFreesUpInTime(t *testing.T) {
	fetcher := &fakeFetcher{}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, Limits{Timeout: 20 * time.Millisecond, MaxConcurrentDownloads: 1})
	service.slots <- struct{}{}

	result, err := service.Ingest(context.Background(), 1342)

	if err != nil || result.Status != models.StatusFailed || !errors.Is(result.Err, models.ErrTransient) {
		t.Errorf("result = %+v, err = %v; want a transient failed result", result, err)
	}
	if calls := fetcher.calls.Load(); calls != 0 {
		t.Errorf("downloads = %d, want 0 while no slot is free", calls)
	}
}

func TestACallerMayStopWaitingWithoutCancellingTheIngestion(t *testing.T) {
	fetcher := &fakeFetcher{release: make(chan struct{})}
	service := newTestService(t, fetcher, newMemoryStore(), testRetry, testLimits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Ingest(ctx, 1342); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	retry := service.join(1342)
	close(fetcher.release)
	<-retry.done

	if retry.result.Status != models.StatusDownloaded || fetcher.calls.Load() != 1 {
		t.Errorf("result = %+v, downloads = %d; want the detached ingestion to finish once", retry.result, fetcher.calls.Load())
	}
}

func TestLocateAndListBookIDsReadTheStore(t *testing.T) {
	service := newTestService(t, &fakeFetcher{}, newMemoryStore(84, 11), testRetry, testLimits)

	if stored, err := service.Locate(84); err != nil || stored.Directory != "datalake/84" {
		t.Errorf("Locate(84) = %+v, %v; want datalake/84", stored, err)
	}
	if _, err := service.Locate(1); !errors.Is(err, models.ErrBookNotFound) {
		t.Errorf("Locate(1) = %v, want ErrBookNotFound", err)
	}
	if bookIDs, err := service.ListBookIDs(); err != nil || !slices.Equal(bookIDs, []models.BookID{11, 84}) {
		t.Errorf("ListBookIDs = %v, %v; want [11 84]", bookIDs, err)
	}
}
