package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"boogle-engine/internal/models"
)

func liveClient(t testing.TB) Client {
	t.Helper()
	config := liveConfig(t)
	client, err := Connect(context.Background(), config, DefaultConnectTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("unexpected close error: %v", err)
		}
	})
	return client
}

func resetDatamart(t testing.TB, client Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	collections := []string{
		BooksCollection,
		InvertedIndexCollection,
		RebuildLocksCollection,
	}
	for _, name := range collections {
		if err := client.Database().Collection(name).Drop(ctx); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
}

func fetchPostings(t *testing.T, client Client, term string) []models.Posting {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var document struct {
		Postings []models.Posting `bson:"postings"`
	}
	err := client.Database().Collection(InvertedIndexCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: term}}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		t.Fatalf("fetch postings for %q: %v", term, err)
	}
	return document.Postings
}

func fetchBookStatus(t *testing.T, client Client, bookID models.BookID) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var document struct {
		Status string `bson:"status"`
	}
	err := client.Database().Collection(BooksCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: bookID}}).Decode(&document)
	if err != nil {
		t.Fatalf("fetch book %d: %v", bookID, err)
	}
	return document.Status
}

func assertPostings(t *testing.T, got []models.Posting, want []models.Posting) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d postings, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i].BookID != want[i].BookID || got[i].TF != want[i].TF {
			t.Fatalf("posting %d: expected %+v, got %+v", i, want[i], got[i])
		}
	}
}

func sampleInput(bookID models.BookID, tokens ...string) models.IndexBookInput {
	return models.IndexBookInput{
		Metadata: models.BookMetadata{
			BookID:          bookID,
			Title:           "Title of Book",
			Author:          "An Author",
			Language:        "en",
			PublicationYear: 1719,
			ReleaseYear:     1994,
		},
		Tokens: tokens,
	}
}

func TestIndexBookStoresTermFrequency(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	err := indexer.IndexBook(context.Background(), sampleInput(42, "island", "ship", "island", "island"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertPostings(t, fetchPostings(t, client, "island"), []models.Posting{{BookID: 42, TF: 3}})
	assertPostings(t, fetchPostings(t, client, "ship"), []models.Posting{{BookID: 42, TF: 1}})
}

func TestIndexBookIsIdempotent(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	input := sampleInput(7, "castaway", "castaway", "desert")

	for run := 1; run <= 3; run++ {
		if err := indexer.IndexBook(context.Background(), input); err != nil {
			t.Fatalf("run %d: unexpected error: %v", run, err)
		}
	}

	assertPostings(t, fetchPostings(t, client, "castaway"), []models.Posting{{BookID: 7, TF: 2}})
	assertPostings(t, fetchPostings(t, client, "desert"), []models.Posting{{BookID: 7, TF: 1}})
}

func TestIndexBookUpdatesTermFrequencyWithoutDuplicating(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if err := indexer.IndexBook(context.Background(), sampleInput(5, "island", "island")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := indexer.IndexBook(context.Background(), sampleInput(5, "island", "island", "island", "island", "island")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertPostings(t, fetchPostings(t, client, "island"), []models.Posting{{BookID: 5, TF: 5}})
}

func TestIndexBookForgetsTermsTheNewVersionDrops(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if err := indexer.IndexBook(context.Background(), sampleInput(4, "island", "island", "wreck")); err != nil {
		t.Fatalf("first version: unexpected error: %v", err)
	}
	if err := indexer.IndexBook(context.Background(), sampleInput(4, "island")); err != nil {
		t.Fatalf("second version: unexpected error: %v", err)
	}

	assertPostings(t, fetchPostings(t, client, "island"), []models.Posting{{BookID: 4, TF: 1}})
	assertPostings(t, fetchPostings(t, client, "wreck"), nil)
}

func TestIndexBookKeepsPostingsSortedAcrossBooks(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	for _, bookID := range []models.BookID{9, 3, 5} {
		if err := indexer.IndexBook(context.Background(), sampleInput(bookID, "island")); err != nil {
			t.Fatalf("book %d: unexpected error: %v", bookID, err)
		}
	}

	assertPostings(t, fetchPostings(t, client, "island"), []models.Posting{
		{BookID: 3, TF: 1},
		{BookID: 5, TF: 1},
		{BookID: 9, TF: 1},
	})
}

func TestIndexBookIsConcurrencySafeForSameBook(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	input := sampleInput(30, "gale", "gale", "shore")

	const workers = 8
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			errs <- indexer.IndexBook(context.Background(), input)
		}()
	}
	for worker := 0; worker < workers; worker++ {
		if err := <-errs; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	assertPostings(t, fetchPostings(t, client, "gale"), []models.Posting{{BookID: 30, TF: 2}})
	if status := fetchBookStatus(t, client, 30); status != StatusPublished {
		t.Fatalf("expected status %q, got %q", StatusPublished, status)
	}
}

func TestIndexBookPublishesMetadata(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if err := indexer.IndexBook(context.Background(), sampleInput(11, "solitude")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status := fetchBookStatus(t, client, 11); status != StatusPublished {
		t.Fatalf("expected status %q, got %q", StatusPublished, status)
	}
}

func TestIndexBookWithholdsUnpublishedBookFromReaders(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if err := indexer.IndexBook(context.Background(), sampleInput(13, "rescue")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count, err := countPublishedBooks(t, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 published book, got %d", count)
	}
}

func countPublishedBooks(t *testing.T, client Client) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Database().Collection(BooksCollection).
		CountDocuments(ctx, bson.D{{Key: "status", Value: StatusPublished}})
}
