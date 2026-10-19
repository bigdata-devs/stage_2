package mongodb

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

func TestRebuildSwapsInNewDatamart(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	for _, bookID := range []models.BookID{1, 2} {
		if err := indexer.IndexBook(context.Background(), sampleInput(bookID, "old")); err != nil {
			t.Fatalf("book %d: unexpected error: %v", bookID, err)
		}
	}

	result, err := indexer.Rebuild(context.Background(), []models.IndexBookInput{
		sampleInput(3, "fresh", "fresh"),
		sampleInput(4, "fresh"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BooksProcessed != 2 {
		t.Fatalf("expected 2 books processed, got %d", result.BooksProcessed)
	}

	assertPostings(t, fetchPostings(t, client, "fresh"), []models.Posting{
		{BookID: 3, TF: 2},
		{BookID: 4, TF: 1},
	})
	if _, err := fetchExistingBook(client, 1); err == nil {
		t.Fatal("expected book 1 to be gone after rebuild, but it is still present")
	}
	if status := fetchBookStatus(t, client, 3); status != StatusPublished {
		t.Fatalf("expected rebuilt book published, got %q", status)
	}
	assertBookIndexesPresent(t, client)
}

func TestRebuildLeavesNoTemporaryCollections(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if _, err := indexer.Rebuild(context.Background(), []models.IndexBookInput{
		sampleInput(8, "solitude"),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{BooksCollection + rebuildSuffix, InvertedIndexCollection + rebuildSuffix} {
		count, err := countDocuments(client, name)
		if err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected %s to be empty after swap, got %d documents", name, count)
		}
	}
}

func TestRebuildIsIdempotent(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	books := []models.IndexBookInput{
		sampleInput(21, "crusoe", "crusoe", "friday"),
		sampleInput(22, "friday"),
	}

	for run := 1; run <= 2; run++ {
		if _, err := indexer.Rebuild(context.Background(), books); err != nil {
			t.Fatalf("run %d: unexpected error: %v", run, err)
		}
	}

	assertPostings(t, fetchPostings(t, client, "crusoe"), []models.Posting{{BookID: 21, TF: 2}})
	assertPostings(t, fetchPostings(t, client, "friday"), []models.Posting{
		{BookID: 21, TF: 1},
		{BookID: 22, TF: 1},
	})
}

func fetchExistingBook(client Client, bookID models.BookID) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var document bson.M
	err := client.Database().Collection(BooksCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: bookID}}).Decode(&document)
	return document, err
}

func countDocuments(client Client, collection string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Database().Collection(collection).CountDocuments(ctx, bson.D{})
}

func assertBookIndexesPresent(t *testing.T, client Client) {
	t.Helper()
	count, err := countBookIndexes(t, client)
	if err != nil {
		t.Fatalf("count book indexes: %v", err)
	}
	if count != len(bookFilterIndexes) {
		t.Fatalf("expected %d book indexes after rebuild, got %d", len(bookFilterIndexes), count)
	}
}
