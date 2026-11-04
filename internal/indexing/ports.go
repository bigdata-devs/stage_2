package indexing

import (
	"context"

	"boogle-engine/internal/models"
)

type BookSource interface {
	ReadBook(bookID models.BookID) (models.Book, error)
	ListBookIDs() ([]models.BookID, error)
}

type NextBook func(ctx context.Context) (models.IndexBookInput, bool, error)

type IndexWriter interface {
	IndexBook(ctx context.Context, input models.IndexBookInput) error
	Rebuild(ctx context.Context, next NextBook) (int, error)
}

type RebuildLease interface {
	Release() error
}

type RebuildLocker interface {
	AcquireRebuildLease(ctx context.Context) (RebuildLease, error)
}

type IndexMetrics interface {
	CountPublishedBooks(ctx context.Context) (int, error)
	DataSizeBytes(ctx context.Context) (uint64, error)
}

type Ports struct {
	Source  BookSource
	Writer  IndexWriter
	Locker  RebuildLocker
	Metrics IndexMetrics
}
