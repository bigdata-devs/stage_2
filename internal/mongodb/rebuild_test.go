package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

func booksOf(books ...models.IndexBookInput) NextBook {
	position := 0
	return func(context.Context) (models.IndexBookInput, bool, error) {
		if position >= len(books) {
			return models.IndexBookInput{}, false, nil
		}
		book := books[position]
		position++
		return book, true, nil
	}
}

func TestRebuildSwapsInNewDatamart(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	for _, bookID := range []models.BookID{1, 2} {
		if err := indexer.IndexBook(context.Background(), sampleInput(bookID, "old")); err != nil {
			t.Fatalf("book %d: unexpected error: %v", bookID, err)
		}
	}

	result, err := indexer.Rebuild(context.Background(), booksOf(
		sampleInput(3, "fresh", "fresh"),
		sampleInput(4, "fresh"),
	))
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
	assertPostingIndexPresent(t, client)
}

func TestRebuildStopsWhenTheStreamFails(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	if err := indexer.IndexBook(context.Background(), sampleInput(1, "kept")); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	calls := 0
	next := func(context.Context) (models.IndexBookInput, bool, error) {
		calls++
		if calls == 2 {
			return models.IndexBookInput{}, false, errors.New("datalake read failed")
		}
		return sampleInput(9, "partial"), true, nil
	}

	if _, err := indexer.Rebuild(context.Background(), next); err == nil {
		t.Fatal("expected the failing stream to fail the rebuild")
	}

	if _, err := fetchExistingBook(client, 1); err != nil {
		t.Fatalf("previous book disappeared after a failed rebuild: %v", err)
	}
	assertPostings(t, fetchPostings(t, client, "kept"), []models.Posting{{BookID: 1, TF: 1}})
	if _, err := fetchExistingBook(client, 9); err == nil {
		t.Fatal("a book of the failed rebuild is visible in the published datamart")
	}
}

func TestRebuildLeavesNoTemporaryCollections(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if _, err := indexer.Rebuild(context.Background(), booksOf(
		sampleInput(8, "solitude"),
	)); err != nil {
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
		if _, err := indexer.Rebuild(context.Background(), booksOf(books...)); err != nil {
			t.Fatalf("run %d: unexpected error: %v", run, err)
		}
	}

	assertPostings(t, fetchPostings(t, client, "crusoe"), []models.Posting{{BookID: 21, TF: 2}})
	assertPostings(t, fetchPostings(t, client, "friday"), []models.Posting{
		{BookID: 21, TF: 1},
		{BookID: 22, TF: 1},
	})
}

func TestRebuildWithNoBooksClearsTheDatamart(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	if err := indexer.IndexBook(context.Background(), sampleInput(1, "stale", "stale")); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	result, err := indexer.Rebuild(context.Background(), booksOf())
	if err != nil {
		t.Fatalf("rebuild of an empty datalake: %v", err)
	}
	if result.BooksProcessed != 0 {
		t.Fatalf("expected 0 books processed, got %d", result.BooksProcessed)
	}

	if _, err := fetchExistingBook(client, 1); err == nil {
		t.Fatal("expected the stale book to be gone after the empty rebuild")
	}
	for _, name := range []string{BooksCollection + rebuildSuffix, InvertedIndexCollection + rebuildSuffix} {
		if count, err := countDocuments(client, name); err != nil || count != 0 {
			t.Fatalf("expected %s to be empty after the swap, got %d documents (err: %v)", name, count, err)
		}
	}
	assertBookIndexesPresent(t, client)
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

func assertPostingIndexPresent(t *testing.T, client Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cursor, err := client.Database().Collection(InvertedIndexCollection).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list posting indexes: %v", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			t.Errorf("close index cursor: %v", err)
		}
	}()
	for cursor.Next(ctx) {
		var document struct {
			Name string `bson:"name"`
		}
		if err := cursor.Decode(&document); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if document.Name != "_id_" {
			return
		}
	}
	t.Fatal("the rebuilt postings collection has no posting index")
}
