package indexer

import (
	"fmt"

	"boogle-engine/internal/models"
)

type BookReader interface {
	ReadBook(bookID models.BookID) (models.Book, error)
}

type Indexer struct {
	reader  BookReader
	catalog *MetadataCatalog
	index   *InvertedIndex
}

func NewIndexer(reader BookReader, catalog *MetadataCatalog, index *InvertedIndex) Indexer {
	return Indexer{reader: reader, catalog: catalog, index: index}
}

func (indexer Indexer) IndexBook(bookID models.BookID) error {
	book, err := indexer.reader.ReadBook(bookID)
	if err != nil {
		return fmt.Errorf("index book %d: %w", bookID, err)
	}
	indexer.index.AddBook(book.ID, ProcessText(book.Body))
	indexer.catalog.Store(ExtractMetadata(book.ID, book.Header))
	return nil
}
