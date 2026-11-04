package indexing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/indexing"
	"boogle-engine/internal/models"
)

var (
	errBookMissing = errors.New("book missing")
	errWriteFailed = errors.New("write failed")
	errSwapFailed  = errors.New("swap failed")
	errListFailed  = errors.New("list failed")
)

type fakeSource struct {
	books   map[models.BookID]models.Book
	listErr error
}

func (source *fakeSource) ReadBook(bookID models.BookID) (models.Book, error) {
	book, found := source.books[bookID]
	if !found {
		return models.Book{}, fmt.Errorf("%w: %d", errBookMissing, bookID)
	}
	return book, nil
}

func (source *fakeSource) ListBookIDs() ([]models.BookID, error) {
	if source.listErr != nil {
		return nil, source.listErr
	}
	bookIDs := make([]models.BookID, 0, len(source.books))
	for bookID := range source.books {
		bookIDs = append(bookIDs, bookID)
	}
	slices.Sort(bookIDs)
	return bookIDs, nil
}

type fakeWriter struct {
	mutex            sync.Mutex
	events           []string
	indexed          []models.IndexBookInput
	rebuilt          []models.BookID
	indexCalls       int
	failingIndexCall int
	rebuildErr       error
	duringRebuild    func()
}

func (writer *fakeWriter) IndexBook(_ context.Context, input models.IndexBookInput) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	writer.indexCalls++
	if writer.indexCalls == writer.failingIndexCall {
		return errWriteFailed
	}
	writer.indexed = append(writer.indexed, input)
	writer.events = append(writer.events, fmt.Sprintf("index %d", input.Metadata.BookID))
	return nil
}

func (writer *fakeWriter) Rebuild(ctx context.Context, next indexing.NextBook) (int, error) {
	writer.record("rebuild start")
	if writer.duringRebuild != nil {
		writer.duringRebuild()
	}
	processed := 0
	for {
		input, found, err := next(ctx)
		if err != nil {
			return 0, err
		}
		if !found {
			break
		}
		writer.mutex.Lock()
		writer.rebuilt = append(writer.rebuilt, input.Metadata.BookID)
		writer.mutex.Unlock()
		processed++
	}
	if writer.rebuildErr != nil {
		return 0, writer.rebuildErr
	}
	writer.record("swap")
	return processed, nil
}

func (writer *fakeWriter) record(event string) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	writer.events = append(writer.events, event)
}

func (writer *fakeWriter) recorded() []string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return slices.Clone(writer.events)
}

type fakeLocker struct {
	mutex    sync.Mutex
	err      error
	acquired int
	released int
}

func (locker *fakeLocker) AcquireRebuildLease(context.Context) (indexing.RebuildLease, error) {
	locker.mutex.Lock()
	defer locker.mutex.Unlock()
	if locker.err != nil {
		return nil, locker.err
	}
	locker.acquired++
	return fakeLease{locker: locker}, nil
}

func (locker *fakeLocker) counts() (int, int) {
	locker.mutex.Lock()
	defer locker.mutex.Unlock()
	return locker.acquired, locker.released
}

type fakeLease struct {
	locker *fakeLocker
}

func (lease fakeLease) Release() error {
	lease.locker.mutex.Lock()
	defer lease.locker.mutex.Unlock()
	lease.locker.released++
	return nil
}

type fakeMetrics struct {
	books int
	bytes uint64
	err   error
}

func (metrics fakeMetrics) CountPublishedBooks(context.Context) (int, error) {
	return metrics.books, metrics.err
}

func (metrics fakeMetrics) DataSizeBytes(context.Context) (uint64, error) {
	return metrics.bytes, metrics.err
}

type fixture struct {
	service *indexing.Service
	source  *fakeSource
	writer  *fakeWriter
	locker  *fakeLocker
}

func newFixture(t *testing.T, books ...models.Book) fixture {
	t.Helper()
	f := fixture{
		source: &fakeSource{books: map[models.BookID]models.Book{}},
		writer: &fakeWriter{},
		locker: &fakeLocker{},
	}
	for _, book := range books {
		f.source.books[book.ID] = book
	}
	f.service = newService(t, indexing.Ports{Source: f.source, Writer: f.writer, Locker: f.locker, Metrics: fakeMetrics{}})
	return f
}

func newService(t *testing.T, ports indexing.Ports) *indexing.Service {
	t.Helper()
	service, err := indexing.NewService(ports)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func sampleBook(bookID models.BookID, title string) models.Book {
	return models.Book{
		ID:     bookID,
		Header: "Title: " + title + "\nAuthor: Jane Austen\nLanguage: English\nOriginal Publication: 1813\n",
		Body:   "A story of curiosity that every reader remembers. The curiosity returns.",
	}
}

func assertEvents(t *testing.T, writer *fakeWriter, want ...string) {
	t.Helper()
	if got := writer.recorded(); !slices.Equal(got, want) {
		t.Fatalf("writer events = %q, want %q", got, want)
	}
}

func assertLeasesBalanced(t *testing.T, locker *fakeLocker, wantAcquired int) {
	t.Helper()
	acquired, released := locker.counts()
	if acquired != wantAcquired || released != wantAcquired {
		t.Fatalf("leases acquired %d and released %d, want %d of each", acquired, released, wantAcquired)
	}
}

func TestNewServiceRejectsMissingPorts(t *testing.T) {
	complete := indexing.Ports{Source: &fakeSource{}, Writer: &fakeWriter{}, Locker: &fakeLocker{}, Metrics: fakeMetrics{}}
	drops := map[string]func(*indexing.Ports){
		"source":  func(ports *indexing.Ports) { ports.Source = nil },
		"writer":  func(ports *indexing.Ports) { ports.Writer = nil },
		"locker":  func(ports *indexing.Ports) { ports.Locker = nil },
		"metrics": func(ports *indexing.Ports) { ports.Metrics = nil },
	}
	for name, drop := range drops {
		ports := complete
		drop(&ports)
		if _, err := indexing.NewService(ports); !errors.Is(err, indexing.ErrMissingPort) {
			t.Errorf("NewService without %s = %v, want ErrMissingPort", name, err)
		}
	}
}

func TestIndexBookWritesTheExtractedMetadataAndTokens(t *testing.T) {
	book := sampleBook(1342, "Pride and Prejudice")
	f := newFixture(t, book)

	if err := f.service.IndexBook(context.Background(), 1342); err != nil {
		t.Fatalf("index book: %v", err)
	}

	if len(f.writer.indexed) != 1 {
		t.Fatalf("writer received %d books, want 1", len(f.writer.indexed))
	}
	got := f.writer.indexed[0]
	if want := indexer.ExtractMetadata(book.ID, book.Header); got.Metadata != want {
		t.Errorf("metadata = %+v, want %+v", got.Metadata, want)
	}
	if want := indexer.ProcessText(book.Body); !slices.Equal(got.Tokens, want) {
		t.Errorf("tokens = %q, want %q", got.Tokens, want)
	}
}

func TestIndexBookOfAMissingBookDoesNotWrite(t *testing.T) {
	f := newFixture(t)

	err := f.service.IndexBook(context.Background(), 999)

	if !errors.Is(err, errBookMissing) {
		t.Fatalf("IndexBook of a missing book = %v, want the source error", err)
	}
	if f.writer.indexCalls != 0 {
		t.Errorf("writer was called %d times for a missing book, want 0", f.writer.indexCalls)
	}
}

func TestIndexBookReportsAWriterFailure(t *testing.T) {
	f := newFixture(t, sampleBook(1, "Sample"))
	f.writer.failingIndexCall = 1

	if err := f.service.IndexBook(context.Background(), 1); !errors.Is(err, errWriteFailed) {
		t.Fatalf("IndexBook with a failing writer = %v, want the writer error", err)
	}
}

func TestRebuildStreamsEveryBookOfTheSourceUnderOneLease(t *testing.T) {
	f := newFixture(t, sampleBook(2, "Second"), sampleBook(1, "First"))

	report, err := f.service.Rebuild(context.Background())

	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.BooksProcessed != 2 {
		t.Errorf("books processed = %d, want 2", report.BooksProcessed)
	}
	if !slices.Equal(f.writer.rebuilt, []models.BookID{1, 2}) {
		t.Errorf("rebuilt books = %v, want [1 2]", f.writer.rebuilt)
	}
	assertLeasesBalanced(t, f.locker, 1)
}

func TestRebuildRefusedByAnotherReplicaReportsRebuildInProgress(t *testing.T) {
	f := newFixture(t, sampleBook(1, "Sample"))
	f.locker.err = fmt.Errorf("%w: lease held by another replica", indexing.ErrRebuildInProgress)

	_, err := f.service.Rebuild(context.Background())

	if !errors.Is(err, indexing.ErrRebuildInProgress) {
		t.Fatalf("Rebuild with a refused lease = %v, want ErrRebuildInProgress", err)
	}
	assertEvents(t, f.writer)

	f.locker.err = nil
	if _, err := f.service.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild after the lease was freed: %v", err)
	}
}

func TestRebuildLeaseFailureIsNotReportedAsRebuildInProgress(t *testing.T) {
	f := newFixture(t)
	leaseErr := errors.New("datamart unreachable")
	f.locker.err = leaseErr

	_, err := f.service.Rebuild(context.Background())

	if !errors.Is(err, leaseErr) || errors.Is(err, indexing.ErrRebuildInProgress) {
		t.Fatalf("Rebuild with a failing locker = %v, want the locker error only", err)
	}
}

func TestRebuildRejectsAnotherRebuildOfTheSameProcess(t *testing.T) {
	f := newFixture(t, sampleBook(1, "Sample"))
	var nestedErr error
	f.writer.duringRebuild = func() { _, nestedErr = f.service.Rebuild(context.Background()) }

	if _, err := f.service.Rebuild(context.Background()); err != nil {
		t.Fatalf("outer rebuild: %v", err)
	}

	if !errors.Is(nestedErr, indexing.ErrRebuildInProgress) {
		t.Fatalf("Rebuild while one is running = %v, want ErrRebuildInProgress", nestedErr)
	}
	assertLeasesBalanced(t, f.locker, 2)
}

func TestUpdatesDuringARebuildAreReplayedAfterTheSwap(t *testing.T) {
	f := newFixture(t, sampleBook(1, "First"), sampleBook(2, "Second"))
	var updateErr error
	f.writer.duringRebuild = func() { updateErr = f.service.IndexBook(context.Background(), 2) }

	if _, err := f.service.Rebuild(context.Background()); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if updateErr != nil {
		t.Fatalf("update during rebuild: %v", updateErr)
	}
	assertEvents(t, f.writer, "rebuild start", "index 2", "swap", "index 2")

	f.writer.duringRebuild = nil
	if _, err := f.service.Rebuild(context.Background()); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	assertEvents(t, f.writer, "rebuild start", "index 2", "swap", "index 2", "rebuild start", "swap")
}

func TestFailedRebuildDropsRecordedUpdatesAndReleasesTheLease(t *testing.T) {
	f := newFixture(t, sampleBook(2, "Sample"))
	f.writer.rebuildErr = errSwapFailed
	f.writer.duringRebuild = func() { _ = f.service.IndexBook(context.Background(), 2) }

	if _, err := f.service.Rebuild(context.Background()); !errors.Is(err, errSwapFailed) {
		t.Fatalf("Rebuild with a failing swap = %v, want the writer error", err)
	}
	assertEvents(t, f.writer, "rebuild start", "index 2")
	assertLeasesBalanced(t, f.locker, 1)

	f.writer.rebuildErr = nil
	f.writer.duringRebuild = nil
	if _, err := f.service.Rebuild(context.Background()); err != nil {
		t.Fatalf("rebuild after the failure: %v", err)
	}
	assertEvents(t, f.writer, "rebuild start", "index 2", "rebuild start", "swap")
}

func TestRebuildFailsWhenTheReplayFails(t *testing.T) {
	f := newFixture(t, sampleBook(2, "Sample"))
	f.writer.failingIndexCall = 2
	f.writer.duringRebuild = func() { _ = f.service.IndexBook(context.Background(), 2) }

	if _, err := f.service.Rebuild(context.Background()); !errors.Is(err, errWriteFailed) {
		t.Fatalf("Rebuild with a failing replay = %v, want the writer error", err)
	}
	assertLeasesBalanced(t, f.locker, 1)
}

func TestRebuildFailsWhenTheSourceCannotBeListed(t *testing.T) {
	f := newFixture(t)
	f.source.listErr = errListFailed

	if _, err := f.service.Rebuild(context.Background()); !errors.Is(err, errListFailed) {
		t.Fatalf("Rebuild with a failing listing = %v, want the source error", err)
	}
	assertEvents(t, f.writer)
	assertLeasesBalanced(t, f.locker, 1)
}

func TestRebuildInterruptedByTheCallerReportsTheContextError(t *testing.T) {
	f := newFixture(t, sampleBook(1, "Sample"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.service.Rebuild(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild with a cancelled context = %v, want context.Canceled", err)
	}
	assertLeasesBalanced(t, f.locker, 1)
}

func TestStatsReportsTheIndexMetrics(t *testing.T) {
	service := newService(t, indexing.Ports{
		Source:  &fakeSource{},
		Writer:  &fakeWriter{},
		Locker:  &fakeLocker{},
		Metrics: fakeMetrics{books: 3, bytes: 4096},
	})

	stats := service.Stats()

	if stats.BooksIndexed != 3 || stats.SizeBytes != 4096 {
		t.Errorf("stats = %+v, want 3 books and 4096 bytes", stats)
	}
	if stats.LastUpdate.IsZero() {
		t.Error("last update is zero")
	}
}

func TestStatsFallsBackToZeroWhenTheMetricsFail(t *testing.T) {
	service := newService(t, indexing.Ports{
		Source:  &fakeSource{},
		Writer:  &fakeWriter{},
		Locker:  &fakeLocker{},
		Metrics: fakeMetrics{books: 3, bytes: 4096, err: errors.New("datamart unreachable")},
	})

	stats := service.Stats()

	if stats.BooksIndexed != 0 || stats.SizeBytes != 0 {
		t.Errorf("stats with failing metrics = %+v, want zero books and bytes", stats)
	}
	if stats.LastUpdate.IsZero() {
		t.Error("last update is zero")
	}
}
