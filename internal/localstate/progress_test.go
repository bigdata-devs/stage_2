package localstate

import (
	"context"
	"path/filepath"
	"testing"

	"boogle-engine/internal/control"
	"boogle-engine/internal/models"
)

type instantIngestion struct{}

func (instantIngestion) Ingest(_ context.Context, bookID models.BookID) (models.IngestionResult, error) {
	return models.IngestionResult{BookID: bookID, Status: models.StatusDownloaded}, nil
}

type instantIndexing struct{}

func (instantIndexing) Update(context.Context, models.BookID) error {
	return nil
}

func runOrchestrator(t *testing.T, store *Store, candidates []models.BookID) control.Summary {
	t.Helper()
	orchestrator, err := control.NewOrchestrator(control.OrchestratorConfig{
		Ingestion:  instantIngestion{},
		Indexing:   instantIndexing{},
		Progress:   store,
		Candidates: candidates,
		Workers:    2,
		Retry:      control.RetryPolicy{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	return orchestrator.Run(context.Background())
}

func TestStoreKeepsTheProgressOfAnOrchestratorRunAcrossRestarts(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "control")
	store, err := Load(directory)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	candidates := []models.BookID{84, 1342}

	if summary := runOrchestrator(t, store, candidates); summary.Indexed != 2 {
		t.Fatalf("first run = %+v, want 2 indexed books", summary)
	}

	reloaded, err := Load(directory)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, bookID := range candidates {
		if !reloaded.IsDownloaded(bookID) || !reloaded.IsIndexed(bookID) {
			t.Errorf("book %d is not recorded as downloaded and indexed after a reload", bookID)
		}
	}
	if summary := runOrchestrator(t, reloaded, candidates); summary.Skipped != 2 || summary.Indexed != 0 {
		t.Fatalf("run after a restart = %+v, want both books skipped", summary)
	}
}
