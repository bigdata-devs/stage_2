package control

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"boogle-engine/internal/models"
)

type fakeIngestion struct {
	mutex   sync.Mutex
	calls   map[models.BookID]int
	respond func(context.Context, models.BookID) (models.IngestionResult, error)
}

const fakeServiceLatency = 5 * time.Millisecond

func newFakeIngestion(respond func(context.Context, models.BookID) (models.IngestionResult, error)) *fakeIngestion {
	return &fakeIngestion{calls: map[models.BookID]int{}, respond: respond}
}

func (fake *fakeIngestion) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	fake.mutex.Lock()
	fake.calls[bookID]++
	fake.mutex.Unlock()
	return fake.respond(ctx, bookID)
}

func (fake *fakeIngestion) callCount(bookID models.BookID) int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.calls[bookID]
}

type fakeIndexing struct {
	mutex   sync.Mutex
	calls   map[models.BookID]int
	respond func(context.Context, models.BookID) (models.IndexUpdateResult, error)
}

func newFakeIndexing(respond func(context.Context, models.BookID) (models.IndexUpdateResult, error)) *fakeIndexing {
	return &fakeIndexing{calls: map[models.BookID]int{}, respond: respond}
}

func (fake *fakeIndexing) Update(ctx context.Context, bookID models.BookID) (models.IndexUpdateResult, error) {
	fake.mutex.Lock()
	fake.calls[bookID]++
	fake.mutex.Unlock()
	return fake.respond(ctx, bookID)
}

func (fake *fakeIndexing) callCount(bookID models.BookID) int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.calls[bookID]
}

func downloadedAnswer(_ context.Context, bookID models.BookID) (models.IngestionResult, error) {
	time.Sleep(fakeServiceLatency)
	return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded}, nil
}

func updatedAnswer(_ context.Context, bookID models.BookID) (models.IndexUpdateResult, error) {
	time.Sleep(fakeServiceLatency)
	return models.IndexUpdateResult{BookID: bookID, Index: "updated"}, nil
}

func alwaysFailIngest(_ context.Context, _ models.BookID) (models.IngestionResult, error) {
	return models.IngestionResult{}, models.ErrTransient
}

func newTestOrchestrator(t *testing.T, ingestion IngestionService, indexing IndexingService,
	state *ControlState, candidates []models.BookID, workers int) Orchestrator {
	t.Helper()
	orchestrator, err := NewOrchestrator(OrchestratorConfig{
		Ingestion:  ingestion,
		Indexing:   indexing,
		State:      state,
		Candidates: candidates,
		Workers:    workers,
		Retry:      RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return orchestrator
}

func TestRunIngestsIndexesAndRecords(t *testing.T) {
	ingestion := newFakeIngestion(downloadedAnswer)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Indexed != 1 || summary.Total() != 1 {
		t.Fatalf("expected 1 indexed book, got %+v", summary)
	}
	if !state.IsDownloaded(1342) || !state.IsIndexed(1342) {
		t.Fatal("expected book 1342 recorded as downloaded and indexed")
	}
	if ingestion.callCount(1342) != 1 || indexing.callCount(1342) != 1 {
		t.Fatalf("expected one call per service, got %d ingest and %d index",
			ingestion.callCount(1342), indexing.callCount(1342))
	}
	if summary.IngestElapsed <= 0 || summary.IndexElapsed <= 0 {
		t.Fatalf("expected positive step durations, got ingest %s and index %s",
			summary.IngestElapsed, summary.IndexElapsed)
	}
}

func TestRunSkipsAlreadyIndexedBooks(t *testing.T) {
	ingestion := newFakeIngestion(downloadedAnswer)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)
	if err := state.RecordIndexed(1342); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Skipped != 1 || summary.Indexed != 0 {
		t.Fatalf("expected 1 skipped book, got %+v", summary)
	}
	if ingestion.callCount(1342) != 0 || indexing.callCount(1342) != 0 {
		t.Fatal("expected no service calls for an already indexed book")
	}
	if summary.IngestElapsed != 0 || summary.IndexElapsed != 0 {
		t.Fatalf("expected zero step durations for skipped books, got ingest %s and index %s",
			summary.IngestElapsed, summary.IndexElapsed)
	}
}

func TestRunSkipsPreviouslyFailedBooks(t *testing.T) {
	ingestion := newFakeIngestion(downloadedAnswer)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)
	if err := state.RecordFailed(5, "book unavailable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{5}, 1).Run(context.Background())

	if summary.Skipped != 1 {
		t.Fatalf("expected 1 skipped book, got %+v", summary)
	}
	if ingestion.callCount(5) != 0 {
		t.Fatal("expected no ingestion call for a previously failed book")
	}
}

func TestRunResumesFromDownloadedState(t *testing.T) {
	ingestion := newFakeIngestion(downloadedAnswer)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)
	if err := state.RecordDownloaded(1342); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Indexed != 1 {
		t.Fatalf("expected 1 indexed book, got %+v", summary)
	}
	if ingestion.callCount(1342) != 0 || indexing.callCount(1342) != 1 {
		t.Fatalf("expected only the index call, got %d ingest and %d index",
			ingestion.callCount(1342), indexing.callCount(1342))
	}
}

func TestRunRetriesTransientFailures(t *testing.T) {
	var mutex sync.Mutex
	attempts := 0
	ingestion := newFakeIngestion(func(_ context.Context, bookID models.BookID) (models.IngestionResult, error) {
		mutex.Lock()
		defer mutex.Unlock()
		attempts++
		if attempts == 1 {
			return models.IngestionResult{}, models.ErrTransient
		}
		return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded}, nil
	})
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Indexed != 1 {
		t.Fatalf("expected 1 indexed book, got %+v", summary)
	}
	if ingestion.callCount(1342) != 2 {
		t.Fatalf("expected 2 ingestion attempts, got %d", ingestion.callCount(1342))
	}
}

func TestRunAbortsRetriesOnPermanentFailure(t *testing.T) {
	ingestion := newFakeIngestion(func(_ context.Context, _ models.BookID) (models.IngestionResult, error) {
		return models.IngestionResult{}, models.ErrPermanent
	})
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Failed != 1 {
		t.Fatalf("expected 1 failed book, got %+v", summary)
	}
	if ingestion.callCount(1342) != 1 {
		t.Fatalf("expected a single attempt for a permanent failure, got %d", ingestion.callCount(1342))
	}
	reason, found := state.FailureReason(1342)
	if !found || reason == "" {
		t.Fatal("expected a recorded failure reason")
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	ingestion := newFakeIngestion(alwaysFailIngest)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Failed != 1 {
		t.Fatalf("expected 1 failed book, got %+v", summary)
	}
	if ingestion.callCount(1342) != 3 {
		t.Fatalf("expected 3 attempts, got %d", ingestion.callCount(1342))
	}
}

func TestRunReportsUnavailableBookAsFailed(t *testing.T) {
	ingestion := newFakeIngestion(func(_ context.Context, bookID models.BookID) (models.IngestionResult, error) {
		return models.IngestionResult{BookID: bookID, Status: models.StatusUnavailable}, nil
	})
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(context.Background())

	if summary.Failed != 1 {
		t.Fatalf("expected 1 failed book, got %+v", summary)
	}
	if indexing.callCount(1342) != 0 {
		t.Fatal("expected no index call for an unavailable book")
	}
	reason, found := state.FailureReason(1342)
	if !found || !strings.Contains(reason, "book unavailable") {
		t.Fatalf("expected unavailable failure reason, got %q", reason)
	}
}

func TestRunLeavesBooksPendingOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ingestion := newFakeIngestion(func(_ context.Context, bookID models.BookID) (models.IngestionResult, error) {
		cancel()
		return models.IngestionResult{}, ctx.Err()
	})
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, []models.BookID{1342}, 1).Run(ctx)

	if summary.Pending != 1 || summary.Indexed != 0 || summary.Failed != 0 {
		t.Fatalf("expected 1 pending book, got %+v", summary)
	}
	if state.IsDownloaded(1342) {
		t.Fatal("expected no download record for an interrupted book")
	}
	if _, failed := state.FailureReason(1342); failed {
		t.Fatal("expected an interrupted book not to be recorded as failed")
	}
}

func TestRunProcessesEveryCandidateAcrossWorkers(t *testing.T) {
	const books = 20
	candidates := make([]models.BookID, books)
	for index := range candidates {
		candidates[index] = models.BookID(index + 1)
	}
	ingestion := newFakeIngestion(downloadedAnswer)
	indexing := newFakeIndexing(updatedAnswer)
	state := newTestState(t)

	summary := newTestOrchestrator(t, ingestion, indexing, state, candidates, 4).Run(context.Background())

	if summary.Indexed != books || summary.Total() != books {
		t.Fatalf("expected %d indexed books, got %+v", books, summary)
	}
	for bookID := models.BookID(1); bookID <= books; bookID++ {
		if ingestion.callCount(bookID) != 1 || indexing.callCount(bookID) != 1 {
			t.Errorf("book %d: expected exactly one call per service, got %d ingest and %d index",
				bookID, ingestion.callCount(bookID), indexing.callCount(bookID))
		}
	}
}

func TestNewOrchestratorValidatesConfig(t *testing.T) {
	valid := OrchestratorConfig{
		Ingestion: newFakeIngestion(downloadedAnswer),
		Indexing:  newFakeIndexing(updatedAnswer),
		State:     newTestState(t),
		Workers:   1,
		Retry:     RetryPolicy{MaxAttempts: 1},
	}
	for name, mutate := range map[string]func(*OrchestratorConfig){
		"zero workers":     func(config *OrchestratorConfig) { config.Workers = 0 },
		"zero attempts":    func(config *OrchestratorConfig) { config.Retry.MaxAttempts = 0 },
		"negative backoff": func(config *OrchestratorConfig) { config.Retry.InitialBackoff = -time.Second },
	} {
		config := valid
		mutate(&config)
		if _, err := NewOrchestrator(config); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := NewOrchestrator(valid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
