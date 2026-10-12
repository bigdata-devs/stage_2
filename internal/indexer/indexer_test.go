package indexer

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"boogle-engine/internal/models"
)

var errMissingBook = errors.New("missing book")

// memoryReader is an in-memory BookReader standing in for the datalake.
type memoryReader map[models.BookID]models.Book

func (reader memoryReader) ReadBook(bookID models.BookID) (models.Book, error) {
	book, found := reader[bookID]
	if !found {
		return models.Book{}, fmt.Errorf("%w: %d", errMissingBook, bookID)
	}
	return book, nil
}

func TestInvertedIndexKeepsPostingsSortedAndUnique(t *testing.T) {
	index := NewInvertedIndex()
	index.AddBook(1342, []string{"adventure", "island", "adventure"})
	index.AddBook(84, []string{"adventure"})
	index.AddBook(1342, []string{"adventure"})
	if postings := index.Postings("adventure"); !slices.Equal(postings, []models.BookID{84, 1342}) {
		t.Fatalf("unexpected postings %v", postings)
	}
	if postings := index.Postings("missing"); postings == nil || len(postings) != 0 {
		t.Fatalf("expected an empty, non-nil list, got %#v", postings)
	}
}

func TestInvertedIndexSupportsConcurrentWriters(t *testing.T) {
	index := NewInvertedIndex()
	var waitGroup sync.WaitGroup
	for bookID := 1; bookID <= 200; bookID++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			index.AddBook(models.BookID(bookID), []string{"shared", fmt.Sprintf("term%d", bookID%7)})
		}()
	}
	waitGroup.Wait()
	if postings := index.Postings("shared"); len(postings) != 200 || !slices.IsSorted(postings) {
		t.Fatalf("expected 200 sorted postings, got %d (sorted=%t)", len(postings), slices.IsSorted(postings))
	}
}

func TestIndexBookFillsCatalogAndIndexIdempotently(t *testing.T) {
	reader := memoryReader{1342: {ID: 1342, Header: "Title: Pride and Prejudice\nAuthor: Jane Austen\nLanguage: English", Body: "Pride and prejudice"}}
	catalog, index := NewMetadataCatalog(), NewInvertedIndex()
	indexer := NewIndexer(reader, catalog, index)
	for range 2 {
		if err := indexer.IndexBook(1342); err != nil {
			t.Fatalf("index: %v", err)
		}
	}
	metadata, found := catalog.Find(1342)
	if !found || metadata.Title != "Pride and Prejudice" || metadata.Language != "en" {
		t.Fatalf("unexpected metadata %+v (found=%t)", metadata, found)
	}
	if catalog.Count() != 1 || index.TermCount() != 2 || !slices.Equal(index.Postings("pride"), []models.BookID{1342}) {
		t.Fatalf("unexpected state: books=%d terms=%d", catalog.Count(), index.TermCount())
	}
}

func TestIndexBookReportsMissingBooks(t *testing.T) {
	indexer := NewIndexer(memoryReader{}, NewMetadataCatalog(), NewInvertedIndex())
	if err := indexer.IndexBook(7); !errors.Is(err, errMissingBook) {
		t.Fatalf("expected errMissingBook, got %v", err)
	}
}
