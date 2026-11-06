package mongodb

import (
	"context"
	"testing"
)

func TestCountPublishedBooksIgnoresPendingBooks(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	ctx := context.Background()
	if err := NewBookIndexer(client).IndexBook(ctx, sampleInput(1, "island")); err != nil {
		t.Fatalf("index book: %v", err)
	}
	pending := sampleInput(2, "ship").Metadata
	if err := writeMetadata(ctx, client.Database().Collection(BooksCollection), pending, StatusPending); err != nil {
		t.Fatalf("write pending book: %v", err)
	}

	count, err := NewDatamartReader(client).CountPublishedBooks(ctx)

	if err != nil {
		t.Fatalf("count published books: %v", err)
	}
	if count != 1 {
		t.Fatalf("published books = %d, want 1", count)
	}
}

func TestDataSizeBytesIsPositiveOnceABookIsIndexed(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	ctx := context.Background()
	if err := NewBookIndexer(client).IndexBook(ctx, sampleInput(1, "island", "ship")); err != nil {
		t.Fatalf("index book: %v", err)
	}

	size, err := NewDatamartReader(client).DataSizeBytes(ctx)

	if err != nil {
		t.Fatalf("measure datamart size: %v", err)
	}
	if size == 0 {
		t.Fatal("datamart size is zero after indexing a book")
	}
}
