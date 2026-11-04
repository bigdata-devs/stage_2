package indexer

import (
	"sync"

	"boogle-engine/internal/models"
)

type MetadataCatalog struct {
	mutex sync.RWMutex
	books map[models.BookID]models.BookMetadata
}

func NewMetadataCatalog() *MetadataCatalog {
	return &MetadataCatalog{books: map[models.BookID]models.BookMetadata{}}
}

func (catalog *MetadataCatalog) Store(metadata models.BookMetadata) {
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()
	catalog.books[metadata.BookID] = metadata
}

func (catalog *MetadataCatalog) Find(bookID models.BookID) (models.BookMetadata, bool) {
	catalog.mutex.RLock()
	defer catalog.mutex.RUnlock()
	metadata, found := catalog.books[bookID]
	return metadata, found
}

func (catalog *MetadataCatalog) Count() int {
	catalog.mutex.RLock()
	defer catalog.mutex.RUnlock()
	return len(catalog.books)
}
