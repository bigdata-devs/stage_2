package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boogle-engine/internal/crawler"
	"boogle-engine/internal/datalake"
	"boogle-engine/internal/models"
)

type fakeGutenberg struct {
	failure   error
	release   chan struct{}
	downloads atomic.Int32
}

func (gutenberg *fakeGutenberg) FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error) {
	gutenberg.downloads.Add(1)
	if err := gutenberg.waitForRelease(ctx); err != nil {
		return models.Book{}, err
	}
	if gutenberg.failure != nil {
		return models.Book{}, gutenberg.failure
	}
	return models.Book{ID: bookID, Header: "Title: Book " + bookID.String(), Body: "Text of book " + bookID.String()}, nil
}

func (gutenberg *fakeGutenberg) waitForRelease(ctx context.Context) error {
	if gutenberg.release == nil {
		return nil
	}
	select {
	case <-gutenberg.release:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", crawler.ErrTransientDownload, ctx.Err())
	}
}

type ingestionFixture struct {
	gutenberg *fakeGutenberg
	lake      datalake.Datalake
	router    *http.ServeMux
}

func newIngestionFixture(t *testing.T, gutenberg *fakeGutenberg) ingestionFixture {
	t.Helper()
	return newIngestionFixtureAt(t, gutenberg, filepath.Join(t.TempDir(), "datalake"))
}

func newIngestionFixtureAt(t *testing.T, gutenberg *fakeGutenberg, datalakeDir string) ingestionFixture {
	t.Helper()
	lake, err := datalake.New(datalakeDir, datalake.DefaultBatchSize)
	if err != nil {
		t.Fatalf("datalake.New: %v", err)
	}
	ingester, err := crawler.NewIngester(gutenberg, lake, crawler.RetryPolicy{MaxAttempts: 1})
	if err != nil {
		t.Fatalf("crawler.NewIngester: %v", err)
	}
	handler, err := NewIngestionHandler(ingester, lake, IngestionLimits{Timeout: 5 * time.Second, MaxConcurrentDownloads: 2})
	if err != nil {
		t.Fatalf("NewIngestionHandler: %v", err)
	}
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	return ingestionFixture{gutenberg: gutenberg, lake: lake, router: router}
}

func (fixture ingestionFixture) send(method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func (fixture ingestionFixture) storeBook(t *testing.T, bookID models.BookID) {
	t.Helper()
	if _, err := fixture.lake.Store(models.Book{ID: bookID, Header: "header", Body: "body"}); err != nil {
		t.Fatalf("store book %d: %v", bookID, err)
	}
}

func assertBody(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	if body := strings.TrimSpace(recorder.Body.String()); body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func assertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	assertStatus(t, recorder, wantStatus)
	if response := decodeBody[ErrorResponse](t, recorder); response.Error != wantCode {
		t.Errorf("error = %+v, want code %s", response, wantCode)
	}
}

func assertDownloads(t *testing.T, gutenberg *fakeGutenberg, want int32) {
	t.Helper()
	if got := gutenberg.downloads.Load(); got != want {
		t.Errorf("downloads = %d, want %d", got, want)
	}
}

func TestIngestDownloadsBookIntoDatalake(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})

	recorder := fixture.send(http.MethodPost, "/ingest/1342")

	assertStatus(t, recorder, http.StatusOK)
	response := decodeBody[IngestionResponse](t, recorder)
	if response.BookID != 1342 || response.Status != models.StatusDownloaded {
		t.Errorf("response = %+v, want book 1342 downloaded", response)
	}
	if !strings.HasSuffix(response.Path, "/batch_1000_1999/1342") || strings.Contains(response.Path, `\`) {
		t.Errorf("path = %q, want a forward-slash path ending in /batch_1000_1999/1342", response.Path)
	}
	if _, err := fixture.lake.Locate(1342); err != nil {
		t.Errorf("book is not complete in the datalake after a 200: %v", err)
	}
}

func TestIngestTwiceReturnsAvailableWithoutDownloadingAgain(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})
	first := decodeBody[IngestionResponse](t, fixture.send(http.MethodPost, "/ingest/84"))

	recorder := fixture.send(http.MethodPost, "/ingest/84")

	assertStatus(t, recorder, http.StatusOK)
	second := decodeBody[IngestionResponse](t, recorder)
	if second.Status != models.StatusAvailable || second.Path != first.Path {
		t.Errorf("second response = %+v, want available at %q", second, first.Path)
	}
	assertDownloads(t, fixture.gutenberg, 1)
}

func TestConcurrentIngestionsOfSameBookShareOneDownload(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{release: make(chan struct{})})
	const callers = 10
	responses := make([]*httptest.ResponseRecorder, callers)
	var started, finished sync.WaitGroup
	for caller := range callers {
		started.Add(1)
		finished.Add(1)
		go func() {
			defer finished.Done()
			started.Done()
			responses[caller] = fixture.send(http.MethodPost, "/ingest/2701")
		}()
	}
	started.Wait()
	time.Sleep(100 * time.Millisecond)
	close(fixture.gutenberg.release)
	finished.Wait()

	assertDownloads(t, fixture.gutenberg, 1)
	assertSamePath(t, responses)
}

func assertSamePath(t *testing.T, responses []*httptest.ResponseRecorder) {
	t.Helper()
	paths := map[string]bool{}
	for _, recorder := range responses {
		assertStatus(t, recorder, http.StatusOK)
		paths[decodeBody[IngestionResponse](t, recorder).Path] = true
	}
	if len(paths) != 1 {
		t.Errorf("callers received %d different paths, want 1: %v", len(paths), paths)
	}
}

func TestIngestUnavailableBookReturnsNotFound(t *testing.T) {
	failure := fmt.Errorf("%w: book 99999 (HTTP 404)", crawler.ErrBookUnavailable)
	fixture := newIngestionFixture(t, &fakeGutenberg{failure: failure})

	assertErrorCode(t, fixture.send(http.MethodPost, "/ingest/99999"), http.StatusNotFound, bookUnavailableCode)
	assertErrorCode(t, fixture.send(http.MethodGet, "/ingest/status/99999"), http.StatusNotFound, bookNotFoundCode)
}

func TestIngestTransientFailureReturnsServiceUnavailable(t *testing.T) {
	failure := fmt.Errorf("%w: book 1342 (HTTP 503)", crawler.ErrTransientDownload)
	fixture := newIngestionFixture(t, &fakeGutenberg{failure: failure})

	assertErrorCode(t, fixture.send(http.MethodPost, "/ingest/1342"), http.StatusServiceUnavailable, downloadFailedCode)
}

func TestIngestStorageFailureReturnsInternalError(t *testing.T) {
	datalakeDir := filepath.Join(t.TempDir(), "datalake")
	if err := os.WriteFile(datalakeDir, []byte("a file where the datalake folder should be"), 0o644); err != nil {
		t.Fatalf("block datalake folder: %v", err)
	}
	fixture := newIngestionFixtureAt(t, &fakeGutenberg{}, datalakeDir)

	assertErrorCode(t, fixture.send(http.MethodPost, "/ingest/1342"), http.StatusInternalServerError, internalErrorCode)
}

func TestEndpointsRejectInvalidBookID(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})
	targets := []string{
		"POST /ingest/abc", "POST /ingest/0", "POST /ingest/-7", "POST /ingest/1.5",
		"POST /ingest/99999999999999999999999", "POST /ingest/list", "GET /ingest/status/12a",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			method, path, _ := strings.Cut(target, " ")
			assertErrorCode(t, fixture.send(method, path), http.StatusBadRequest, invalidBookIDCode)
		})
	}
	assertDownloads(t, fixture.gutenberg, 0)
}

func TestStatusReportsAvailableBook(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})
	fixture.storeBook(t, 1342)

	recorder := fixture.send(http.MethodGet, "/ingest/status/1342")

	assertStatus(t, recorder, http.StatusOK)
	assertBody(t, recorder, `{"book_id":1342,"status":"available"}`)
}

func TestListReturnsCompleteBooksInAscendingOrder(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})
	assertBody(t, fixture.send(http.MethodGet, "/ingest/list"), `{"count":0,"books":[]}`)
	for _, bookID := range []models.BookID{1342, 11, 84} {
		fixture.storeBook(t, bookID)
	}

	recorder := fixture.send(http.MethodGet, "/ingest/list")

	assertStatus(t, recorder, http.StatusOK)
	assertBody(t, recorder, `{"count":3,"books":[11,84,1342]}`)
}

func TestRoutesRejectWrongMethods(t *testing.T) {
	fixture := newIngestionFixture(t, &fakeGutenberg{})

	recorder := fixture.send(http.MethodGet, "/ingest/1342")

	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "POST" {
		t.Errorf("GET /ingest/1342 = %d (Allow %q), want 405 allowing POST", recorder.Code, recorder.Header().Get("Allow"))
	}
}

type heldIngester struct {
	release chan struct{}
	calls   atomic.Int32
}

func (ingester *heldIngester) Ingest(_ context.Context, bookID models.BookID) models.IngestionResult {
	ingester.calls.Add(1)
	<-ingester.release
	return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded, Path: "datalake/" + bookID.String()}
}

func TestCoalescerJoinsTheIngestionInProgress(t *testing.T) {
	ingester := &heldIngester{release: make(chan struct{})}
	coalescer := newIngestionCoalescer(ingester, IngestionLimits{Timeout: time.Second, MaxConcurrentDownloads: 4})

	first, second, otherBook := coalescer.join(1342), coalescer.join(1342), coalescer.join(84)
	close(ingester.release)
	<-first.done
	<-otherBook.done

	if first != second || first == otherBook {
		t.Errorf("same book must share one ingestion and different books must not")
	}
	if calls := ingester.calls.Load(); calls != 2 {
		t.Errorf("ingester calls = %d, want 2 (one per distinct book)", calls)
	}
}

func TestCoalescerReportsTransientFailureWhenNoSlotFreesUpInTime(t *testing.T) {
	ingester := &heldIngester{release: make(chan struct{})}
	coalescer := newIngestionCoalescer(ingester, IngestionLimits{Timeout: 20 * time.Millisecond, MaxConcurrentDownloads: 1})
	coalescer.slots <- struct{}{}

	result, err := coalescer.Ingest(context.Background(), 1342)

	if err != nil || result.Status != models.StatusFailed || !errors.Is(result.Err, crawler.ErrTransientDownload) {
		t.Errorf("result = %+v, err = %v; want a transient failed result", result, err)
	}
	if calls := ingester.calls.Load(); calls != 0 {
		t.Errorf("ingester calls = %d, want 0 while no slot is free", calls)
	}
}

func TestCoalescerLetsCallerStopWaitingWithoutCancellingTheIngestion(t *testing.T) {
	ingester := &heldIngester{release: make(chan struct{})}
	coalescer := newIngestionCoalescer(ingester, IngestionLimits{Timeout: time.Second, MaxConcurrentDownloads: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := coalescer.Ingest(ctx, 1342); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	retry := coalescer.join(1342)
	close(ingester.release)
	<-retry.done

	if retry.result.Status != models.StatusDownloaded || ingester.calls.Load() != 1 {
		t.Errorf("result = %+v, calls = %d; want the detached ingestion to finish once", retry.result, ingester.calls.Load())
	}
}

func TestNewIngestionHandlerValidatesItsDependencies(t *testing.T) {
	lake, _ := datalake.New(t.TempDir(), datalake.DefaultBatchSize)
	limits := IngestionLimits{Timeout: time.Second, MaxConcurrentDownloads: 1}
	if _, err := NewIngestionHandler(nil, lake, limits); !errors.Is(err, ErrMissingIngestionDependency) {
		t.Errorf("missing ingester: err = %v, want ErrMissingIngestionDependency", err)
	}
	for _, invalid := range []IngestionLimits{{MaxConcurrentDownloads: 1}, {Timeout: time.Second}} {
		if _, err := NewIngestionHandler(&heldIngester{}, lake, invalid); err == nil {
			t.Errorf("limits %+v were accepted", invalid)
		}
	}
}
