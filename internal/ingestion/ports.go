package ingestion

import (
	"context"

	"boogle-engine/internal/models"
)

type BookIngester interface {
	Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error)
}

type DatalakeReader interface {
	Locate(bookID models.BookID) (models.StoredBook, error)
	ListBookIDs() ([]models.BookID, error)
}

type BookFetcher interface {
	FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error)
}

type BookStore interface {
	Locate(bookID models.BookID) (models.StoredBook, error)
	Store(book models.Book) (models.StoredBook, error)
	ListBookIDs() ([]models.BookID, error)
}

type Ports struct {
	Fetcher BookFetcher
	Store   BookStore
}
