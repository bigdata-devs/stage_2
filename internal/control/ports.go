package control

import (
	"context"

	"boogle-engine/internal/models"
)

type IngestionService interface {
	Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error)
}

type IndexingService interface {
	Update(ctx context.Context, bookID models.BookID) error
}

type ProgressStore interface {
	IsDownloaded(bookID models.BookID) bool
	IsIndexed(bookID models.BookID) bool
	FailureReason(bookID models.BookID) (string, bool)
	RecordDownloaded(bookID models.BookID) error
	RecordIndexed(bookID models.BookID) error
	RecordFailed(bookID models.BookID, reason string) error
}

type SearchService interface {
	IsPublished(ctx context.Context, bookID models.BookID) (bool, error)
}

type IndexedBookLister interface {
	IndexedIDs() []models.BookID
}

type FailureResetter interface {
	WipeFailures() ([]models.BookID, error)
}
