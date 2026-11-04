package mongodb

import (
	"context"
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

func TestFilterOptionsListsPublishedValues(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	published := []models.IndexBookInput{
		metadataInput(1, "Jane Austen", "en", 1813, "adventure"),
		metadataInput(2, "Alex Dumas", "en,fr", 1850, "adventure"),
		metadataInput(4, "Unknown Author", "es", 0, "adventure"),
	}
	for _, input := range published {
		if err := indexer.IndexBook(context.Background(), input); err != nil {
			t.Fatalf("book %d: %v", input.Metadata.BookID, err)
		}
	}
	seedPendingBook(t, client, metadataInput(3, "Mark Twain", "de", 1901, "castaway"))

	values, err := NewDatamartReader(client).FilterOptions(context.Background())
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	if len(values.Languages) != 3 || values.Languages[0] != "en" || values.Languages[1] != "es" || values.Languages[2] != "fr" {
		t.Errorf("languages = %v, want [en es fr]", values.Languages)
	}
	if len(values.Years) != 2 || values.Years[0] != 1813 || values.Years[1] != 1850 {
		t.Errorf("years = %v, want [1813 1850]", values.Years)
	}
}

func TestFilterOptionsOnAnEmptyDatamartReturnsEmptyLists(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)

	values, err := NewDatamartReader(client).FilterOptions(context.Background())
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	if values.Languages == nil || values.Years == nil {
		t.Fatalf("nil slices must serialize as [], got %+v", values)
	}
	if len(values.Languages) != 0 || len(values.Years) != 0 {
		t.Errorf("expected empty filter values, got %+v", values)
	}
}
