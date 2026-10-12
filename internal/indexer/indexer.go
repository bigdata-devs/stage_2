// Package indexer turns books stored in the datalake into searchable structures: a metadata
// record per book and an inverted index of body terms. It ports the Stage 1 Go indexing PoC
// (tokenizer and postings) and the Python header parser into one package.
package indexer

import (
	"fmt"

	"boogle-engine/internal/models"
)

// BookReader loads a stored book. datalake.Datalake implements it.
type BookReader interface {
	ReadBook(bookID models.BookID) (models.Book, error)
}

// Indexer indexes datalake books into a metadata catalog and an inverted index.
// It is safe for concurrent use, so several workers can index different books at once.
type Indexer struct {
	reader  BookReader
	catalog *MetadataCatalog
	index   *InvertedIndex
}

// NewIndexer wires a book source to the structures it fills.
func NewIndexer(reader BookReader, catalog *MetadataCatalog, index *InvertedIndex) Indexer {
	return Indexer{reader: reader, catalog: catalog, index: index}
}

// IndexBook reads a book, stores its header metadata and adds its body terms to the inverted
// index. Indexing the same book twice leaves both structures unchanged.
func (indexer Indexer) IndexBook(bookID models.BookID) error {
	book, err := indexer.reader.ReadBook(bookID)
	if err != nil {
		return fmt.Errorf("index book %d: %w", bookID, err)
	}
	indexer.index.AddBook(book.ID, ProcessText(book.Body))
	indexer.catalog.Store(ExtractMetadata(book.ID, book.Header))
	return nil
}
