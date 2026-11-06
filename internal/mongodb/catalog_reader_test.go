package mongodb

import (
	"context"
	"slices"
	"testing"

	"boogle-engine/internal/models"
)

func TestPublishedBookReturnsOnlyPublishedBooks(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	reader := NewDatamartReader(client)
	if err := NewBookIndexer(client).IndexBook(context.Background(),
		metadataInput(1, "Jane Austen", "en", 1813, "adventure")); err != nil {
		t.Fatalf("index published book: %v", err)
	}
	seedPendingBook(t, client, metadataInput(3, "Mark Twain", "es", 1901, "castaway"))

	book, found, err := reader.PublishedBook(context.Background(), 1)
	if err != nil {
		t.Fatalf("published book: %v", err)
	}
	if !found {
		t.Fatal("expected published book 1 to be found")
	}
	if book.Title != "Book 1" || book.Author != "Jane Austen" || book.PublicationYear != 1813 {
		t.Errorf("unexpected metadata: %+v", book)
	}

	if _, found, err = reader.PublishedBook(context.Background(), 3); err != nil {
		t.Fatalf("pending book: %v", err)
	} else if found {
		t.Error("a book awaiting publication must not be visible")
	}

	if _, found, err = reader.PublishedBook(context.Background(), 9999); err != nil {
		t.Fatalf("unknown book: %v", err)
	} else if found {
		t.Error("an unknown book must not be found")
	}
}

func TestDistinctFilterValuesReturnsTheStoredValuesOfPublishedBooks(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	published := []models.IndexBookInput{
		metadataInput(1, "Jane Austen", "en", 1813, "adventure"),
		metadataInput(2, "Alex Dumas", "en,fr", 1850, "adventure"),
		metadataInput(4, "Unknown Author", "es", 0, "adventure"),
		metadataInput(5, "Anonymous", "Unknown", 1900, "adventure"),
	}
	for _, input := range published {
		if err := indexer.IndexBook(context.Background(), input); err != nil {
			t.Fatalf("book %d: %v", input.Metadata.BookID, err)
		}
	}
	seedPendingBook(t, client, metadataInput(3, "Mark Twain", "de", 1901, "castaway"))

	stored, err := NewDatamartReader(client).DistinctFilterValues(context.Background())
	if err != nil {
		t.Fatalf("distinct filter values: %v", err)
	}
	slices.Sort(stored.LanguageLists)
	slices.Sort(stored.Years)
	if want := []string{"Unknown", "en", "en,fr", "es"}; !slices.Equal(stored.LanguageLists, want) {
		t.Errorf("language lists = %q, want the stored values of published books %q", stored.LanguageLists, want)
	}
	if want := []int{0, 1813, 1850, 1900}; !slices.Equal(stored.Years, want) {
		t.Errorf("years = %v, want the stored values of published books %v", stored.Years, want)
	}
}

func TestDistinctFilterValuesOnAnEmptyDatamartReturnsEmptyLists(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)

	stored, err := NewDatamartReader(client).DistinctFilterValues(context.Background())
	if err != nil {
		t.Fatalf("distinct filter values: %v", err)
	}
	if stored.LanguageLists == nil || stored.Years == nil {
		t.Fatalf("nil slices must serialize as [], got %+v", stored)
	}
	if len(stored.LanguageLists) != 0 || len(stored.Years) != 0 {
		t.Errorf("expected empty filter values, got %+v", stored)
	}
}
