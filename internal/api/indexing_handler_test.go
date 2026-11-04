package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"boogle-engine/internal/datalake"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

type indexingFixture struct {
	lake   datalake.Datalake
	router *http.ServeMux
}

func newIndexingFixture(t *testing.T, storedBookIDs ...models.BookID) indexingFixture {
	t.Helper()
	lake, err := datalake.New(filepath.Join(t.TempDir(), "datalake"), datalake.DefaultBatchSize)
	if err != nil {
		t.Fatalf("datalake.New: %v", err)
	}
	for _, bookID := range storedBookIDs {
		storeSampleBook(t, lake, bookID)
	}
	return indexingFixture{lake: lake, router: newIndexingRouter(t, newFakeIndex(lake), time.Minute, testMaxConcurrentUpdates)}
}

type fakeIndex struct {
	lake  datalake.Datalake
	mutex sync.Mutex
	books map[models.BookID]bool
}

func newFakeIndex(lake datalake.Datalake) *fakeIndex {
	return &fakeIndex{lake: lake, books: map[models.BookID]bool{}}
}

func (index *fakeIndex) IndexBook(_ context.Context, bookID models.BookID) error {
	if _, err := index.lake.ReadBook(bookID); err != nil {
		return err
	}
	index.mutex.Lock()
	defer index.mutex.Unlock()
	index.books[bookID] = true
	return nil
}

func (index *fakeIndex) Rebuild(_ context.Context) (indexer.RebuildReport, error) {
	bookIDs, err := index.lake.ListBookIDs()
	if err != nil {
		return indexer.RebuildReport{}, err
	}
	index.mutex.Lock()
	defer index.mutex.Unlock()
	index.books = map[models.BookID]bool{}
	for _, bookID := range bookIDs {
		index.books[bookID] = true
	}
	return indexer.RebuildReport{BooksProcessed: len(bookIDs)}, nil
}

func (index *fakeIndex) Stats() indexer.IndexStats {
	index.mutex.Lock()
	defer index.mutex.Unlock()
	return indexer.IndexStats{BooksIndexed: len(index.books), LastUpdate: time.Now()}
}

func storeSampleBook(t *testing.T, lake datalake.Datalake, bookID models.BookID) {
	t.Helper()
	book := models.Book{
		ID:     bookID,
		Header: fmt.Sprintf("Title: Sample Book %d\nAuthor: Jane Austen\nLanguage: English", bookID),
		Body:   "It is a truth universally acknowledged that a single man must be in want of a wife.",
	}
	if _, err := lake.Store(book); err != nil {
		t.Fatalf("store book %d: %v", bookID, err)
	}
}

const testMaxConcurrentUpdates = 4

func newIndexingRouter(t *testing.T, index BookIndex, rebuildTimeout time.Duration, maxConcurrentUpdates int) *http.ServeMux {
	t.Helper()
	handler, err := NewIndexingHandler(index, rebuildTimeout, maxConcurrentUpdates)
	if err != nil {
		t.Fatalf("NewIndexingHandler: %v", err)
	}
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	return router
}

func serveRequest(router *http.ServeMux, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func (fixture indexingFixture) send(method, target string) *httptest.ResponseRecorder {
	return serveRequest(fixture.router, method, target)
}

func (fixture indexingFixture) booksIndexed(t *testing.T) int {
	t.Helper()
	recorder := fixture.send(http.MethodGet, "/index/status")
	assertStatus(t, recorder, http.StatusOK)
	return decodeBody[IndexStatusResponse](t, recorder).BooksIndexed
}

func assertBooksIndexed(t *testing.T, fixture indexingFixture, want int) {
	t.Helper()
	if got := fixture.booksIndexed(t); got != want {
		t.Errorf("books_indexed = %d, want %d", got, want)
	}
}

func TestIndexUpdateIndexesBookFromDatalake(t *testing.T) {
	fixture := newIndexingFixture(t, 1342)

	recorder := fixture.send(http.MethodPost, "/index/update/1342")

	assertStatus(t, recorder, http.StatusOK)
	assertBody(t, recorder, `{"book_id":1342,"index":"updated"}`)
	assertBooksIndexed(t, fixture, 1)
}

func TestIndexUpdateRepeatedIsIdempotent(t *testing.T) {
	fixture := newIndexingFixture(t, 1342)

	for attempt := 1; attempt <= 3; attempt++ {
		recorder := fixture.send(http.MethodPost, "/index/update/1342")
		assertStatus(t, recorder, http.StatusOK)
		assertBody(t, recorder, `{"book_id":1342,"index":"updated"}`)
	}

	assertBooksIndexed(t, fixture, 1)
}

func TestConcurrentIndexUpdatesOfSameBookCreateNoDuplicates(t *testing.T) {
	fixture := newIndexingFixture(t, 1342, 84)
	var callers sync.WaitGroup
	for caller := range 20 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			target := []string{"/index/update/1342", "/index/update/84"}[caller%2]
			if recorder := fixture.send(http.MethodPost, target); recorder.Code != http.StatusOK {
				t.Errorf("POST %s = %d: %s", target, recorder.Code, recorder.Body.String())
			}
		}()
	}
	callers.Wait()

	assertBooksIndexed(t, fixture, 2)
}

func TestIndexUpdateOfBookMissingFromDatalakeReturnsNotFound(t *testing.T) {
	fixture := newIndexingFixture(t)

	assertErrorCode(t, fixture.send(http.MethodPost, "/index/update/1342"), http.StatusNotFound, bookNotFoundCode)
	assertBooksIndexed(t, fixture, 0)
}

func TestIndexUpdateRejectsInvalidBookID(t *testing.T) {
	fixture := newIndexingFixture(t)
	for _, target := range []string{"/index/update/abc", "/index/update/0", "/index/update/-1", "/index/update/1.5"} {
		t.Run(target, func(t *testing.T) {
			assertErrorCode(t, fixture.send(http.MethodPost, target), http.StatusBadRequest, invalidBookIDCode)
		})
	}
}

func TestRebuildIndexesEveryBookInDatalake(t *testing.T) {
	fixture := newIndexingFixture(t, 11, 84, 1342)

	recorder := fixture.send(http.MethodPost, "/index/rebuild")

	assertStatus(t, recorder, http.StatusOK)
	response := decodeBody[RebuildResponse](t, recorder)
	if _, err := time.ParseDuration(response.ElapsedTime); err != nil || response.BooksProcessed != 3 {
		t.Errorf("response = %+v (parse error %v), want 3 books and a Go duration", response, err)
	}
	assertBooksIndexed(t, fixture, 3)
}

func TestRebuildStartsFromScratch(t *testing.T) {
	fixture := newIndexingFixture(t, 11, 84)
	fixture.send(http.MethodPost, "/index/update/11")
	fixture.send(http.MethodPost, "/index/update/84")
	if err := os.RemoveAll(fixture.lake.BookDirectory(84)); err != nil {
		t.Fatalf("remove book 84 from the datalake: %v", err)
	}

	assertStatus(t, fixture.send(http.MethodPost, "/index/rebuild"), http.StatusOK)

	assertBooksIndexed(t, fixture, 1)
}

type scriptedIndex struct {
	indexErr       error
	rebuildErr     error
	waitForContext bool
	stats          indexer.IndexStats
}

func (index scriptedIndex) IndexBook(context.Context, models.BookID) error {
	return index.indexErr
}

func (index scriptedIndex) Rebuild(ctx context.Context) (indexer.RebuildReport, error) {
	if index.waitForContext {
		<-ctx.Done()
		return indexer.RebuildReport{}, fmt.Errorf("rebuild index: %w", ctx.Err())
	}
	return indexer.RebuildReport{}, index.rebuildErr
}

func (index scriptedIndex) Stats() indexer.IndexStats {
	return index.stats
}

func TestRebuildFailuresKeepTheContract(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"another rebuild running", indexer.ErrRebuildInProgress, http.StatusConflict, rebuildInProgressCode},
		{"caller disconnected", fmt.Errorf("rebuild index: %w", context.Canceled), http.StatusServiceUnavailable, rebuildInterruptedCode},
		{"unreadable datalake", errors.New("list datalake books: permission denied"), http.StatusInternalServerError, internalErrorCode},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			router := newIndexingRouter(t, scriptedIndex{rebuildErr: testCase.err}, time.Minute, testMaxConcurrentUpdates)
			assertErrorCode(t, serveRequest(router, http.MethodPost, "/index/rebuild"), testCase.wantStatus, testCase.wantCode)
		})
	}
}

func TestRebuildIsStoppedByTheRebuildBudget(t *testing.T) {
	router := newIndexingRouter(t, scriptedIndex{waitForContext: true}, 20*time.Millisecond, testMaxConcurrentUpdates)

	recorder := serveRequest(router, http.MethodPost, "/index/rebuild")

	assertErrorCode(t, recorder, http.StatusServiceUnavailable, rebuildInterruptedCode)
}

func TestIndexUpdateFailureReturnsInternalError(t *testing.T) {
	router := newIndexingRouter(t, scriptedIndex{indexErr: errors.New("disk read failed")}, time.Minute, testMaxConcurrentUpdates)

	assertErrorCode(t, serveRequest(router, http.MethodPost, "/index/update/1342"), http.StatusInternalServerError, internalErrorCode)
}

func TestStatusMatchesTheContractExample(t *testing.T) {
	indexSizeMB := 42.7
	stats := indexer.IndexStats{
		BooksIndexed: 1200,
		LastUpdate:   time.Date(2025, 10, 8, 15, 5, 0, 789_000_000, time.FixedZone("CET", 3600)),
		SizeBytes:    uint64(indexSizeMB * bytesPerMegabyte),
	}
	router := newIndexingRouter(t, scriptedIndex{stats: stats}, time.Minute, testMaxConcurrentUpdates)

	recorder := serveRequest(router, http.MethodGet, "/index/status")

	assertStatus(t, recorder, http.StatusOK)
	assertBody(t, recorder, `{"books_indexed":1200,"last_update":"2025-10-08T14:05:00Z","index_size_MB":42.7}`)
}

func TestNewIndexingHandlerValidatesItsArguments(t *testing.T) {
	if _, err := NewIndexingHandler(nil, time.Minute, testMaxConcurrentUpdates); !errors.Is(err, ErrMissingIndex) {
		t.Errorf("missing index: err = %v, want ErrMissingIndex", err)
	}
	if _, err := NewIndexingHandler(scriptedIndex{}, 0, testMaxConcurrentUpdates); err == nil {
		t.Error("a zero rebuild timeout was accepted")
	}
	if _, err := NewIndexingHandler(scriptedIndex{}, time.Minute, 0); err == nil {
		t.Error("a zero max concurrent updates was accepted")
	}
}

type gatedIndex struct {
	entered chan struct{}
	gate    chan struct{}
}

func (index *gatedIndex) IndexBook(ctx context.Context, _ models.BookID) error {
	index.entered <- struct{}{}
	select {
	case <-index.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (index *gatedIndex) Rebuild(context.Context) (indexer.RebuildReport, error) {
	return indexer.RebuildReport{}, nil
}

func (index *gatedIndex) Stats() indexer.IndexStats {
	return indexer.IndexStats{LastUpdate: time.Now()}
}

func startUpdate(router *http.ServeMux, target string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveRequest(router, http.MethodPost, target) }()
	return done
}

func TestIndexUpdateWaitsForAFreeUpdateSlot(t *testing.T) {
	index := &gatedIndex{entered: make(chan struct{}, 4), gate: make(chan struct{})}
	router := newIndexingRouter(t, index, time.Minute, 1)

	first := startUpdate(router, "/index/update/1342")
	<-index.entered

	second := startUpdate(router, "/index/update/84")
	select {
	case <-index.entered:
		t.Fatal("a second update started while -max-concurrent-updates was 1")
	case <-time.After(100 * time.Millisecond):
	}

	close(index.gate)
	if recorder := <-first; recorder.Code != http.StatusOK {
		t.Errorf("first update = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := <-second; recorder.Code != http.StatusOK {
		t.Errorf("queued update = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestIndexUpdateGivesUpWhileWaitingForAnUpdateSlot(t *testing.T) {
	index := &gatedIndex{entered: make(chan struct{}, 4), gate: make(chan struct{})}
	router := newIndexingRouter(t, index, time.Minute, 1)

	first := startUpdate(router, "/index/update/1342")
	<-index.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/index/update/84", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertErrorCode(t, recorder, http.StatusInternalServerError, internalErrorCode)
	select {
	case <-index.entered:
		t.Fatal("the caller that gave up still reached the index")
	default:
	}

	close(index.gate)
	if recorder := <-first; recorder.Code != http.StatusOK {
		t.Errorf("first update = %d: %s", recorder.Code, recorder.Body.String())
	}
}
