package indexer

import (
	"sync"

	"boogle-engine/internal/models"
)

// MetadataCatalog keeps the metadata record of every indexed book, keyed by book ID.
// It is the in-memory counterpart of the Stage 1 SQLite metadata datamart and is safe
// for concurrent use.
type MetadataCatalog struct {
	mutex sync.RWMutex
	books map[models.BookID]models.BookMetadata
}

// NewMetadataCatalog returns an empty catalog.
func NewMetadataCatalog() *MetadataCatalog {
	return &MetadataCatalog{books: map[models.BookID]models.BookMetadata{}}
}

// Store inserts a record, replacing any previous record of the same book.
func (catalog *MetadataCatalog) Store(metadata models.BookMetadata) {
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()
	catalog.books[metadata.BookID] = metadata
}

// Find returns the record of a book and whether the catalog holds it.
func (catalog *MetadataCatalog) Find(bookID models.BookID) (models.BookMetadata, bool) {
	catalog.mutex.RLock()
	defer catalog.mutex.RUnlock()
	metadata, found := catalog.books[bookID]
	return metadata, found
}

// Count returns the number of books in the catalog.
func (catalog *MetadataCatalog) Count() int {
	catalog.mutex.RLock()
	defer catalog.mutex.RUnlock()
	return len(catalog.books)
}
