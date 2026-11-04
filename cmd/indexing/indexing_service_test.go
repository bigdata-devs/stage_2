package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"boogle-engine/internal/datalake"
	"boogle-engine/internal/indexing"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

var (
	liveMongoConfig    mongodb.Config
	liveMongoAvailable bool
)

func TestMain(m *testing.M) {
	liveMongoConfig = mongodb.ConfigFromEnvironment()
	liveMongoAvailable = mongodb.Available(context.Background(), liveMongoConfig)
	if !liveMongoAvailable {
		fmt.Fprintf(os.Stderr, "MongoDB is not available at %s; the live tests of this package will skip (start it with: docker run -d --name boogle-mongo -p 27017:27017 mongo:7)\n", mongodb.RedactURI(liveMongoConfig.URI))
	}
	os.Exit(m.Run())
}

func datamartTestStart(t *testing.T) mongodb.Client {
	t.Helper()
	if !liveMongoAvailable {
		t.Skipf("MongoDB is not available at %s; start it with: docker run -d --name boogle-mongo -p 27017:27017 mongo:7", mongodb.RedactURI(liveMongoConfig.URI))
	}
	t.Setenv("MONGO_DATABASE", "boogle_indexing_bridge_test")
	config := mongodb.ConfigFromEnvironment()
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	client, err := mongodb.Connect(ctx, config, mongodb.DefaultConnectTimeout)
	if err != nil {
		t.Fatalf("connect to datamart: %v", err)
	}
	t.Cleanup(func() {
		resetDatamart(t, client)
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("close datamart: %v", err)
		}
	})
	resetDatamart(t, client)
	if err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return client
}

func resetDatamart(t *testing.T, client mongodb.Client) {
	t.Helper()
	ctx := context.Background()
	for _, collection := range []string{
		mongodb.BooksCollection,
		mongodb.InvertedIndexCollection,
		mongodb.BooksCollection + "_rebuild",
		mongodb.InvertedIndexCollection + "_rebuild",
		mongodb.RebuildLocksCollection,
	} {
		if err := client.Database().Collection(collection).Drop(ctx); err != nil {
			t.Errorf("drop collection %s: %v", collection, err)
		}
	}
}

func sampleBook(bookID models.BookID, title string, term string) models.Book {
	return models.Book{
		ID: bookID,
		Header: "Title: " + title + "\n" +
			"Author: Jane Austen\n" +
			"Language: English\n" +
			"Original Publication: 1813\n",
		Body: "A story of " + term + " that every reader remembers. The " + term + " returns.",
	}
}

func storeBooks(t *testing.T, books ...models.Book) datalake.Datalake {
	t.Helper()
	lake, err := datalake.New(filepath.Join(t.TempDir(), "datalake"), datalake.DefaultBatchSize)
	if err != nil {
		t.Fatalf("create datalake: %v", err)
	}
	for _, book := range books {
		if _, err := lake.Store(book); err != nil {
			t.Fatalf("store book %d: %v", book.ID, err)
		}
	}
	return lake
}

func publishedCount(t *testing.T, client mongodb.Client) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := client.Database().Collection(mongodb.BooksCollection).
		CountDocuments(ctx, map[string]any{"status": mongodb.StatusPublished})
	if err != nil {
		t.Fatalf("count published books: %v", err)
	}
	return int(count)
}

func newWiredService(t *testing.T, lake datalake.Datalake, client mongodb.Client) *indexing.Service {
	t.Helper()
	service, err := newIndexingService(lake, client)
	if err != nil {
		t.Fatalf("wire indexing service: %v", err)
	}
	return service
}

func TestIndexingServicePublishesBook(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t, sampleBook(1342, "Pride and Prejudice", "curiosity"))
	index := newWiredService(t, lake, client)

	if err := index.IndexBook(context.Background(), 1342); err != nil {
		t.Fatalf("index book: %v", err)
	}

	stats := index.Stats()
	if stats.BooksIndexed != 1 {
		t.Errorf("books_indexed = %d, want 1", stats.BooksIndexed)
	}
	if publishedCount(t, client) != 1 {
		t.Errorf("published books in datamart = %d, want 1", publishedCount(t, client))
	}
	if stats.LastUpdate.IsZero() {
		t.Error("last_update is zero after an update")
	}
	if stats.SizeBytes == 0 {
		t.Error("index_size is zero after an update")
	}
}

func TestIndexingServiceIsIdempotent(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t, sampleBook(42, "Sample", "curiosity"))
	index := newWiredService(t, lake, client)

	for run := range 3 {
		if err := index.IndexBook(context.Background(), 42); err != nil {
			t.Fatalf("index book, run %d: %v", run, err)
		}
	}

	if count := index.Stats().BooksIndexed; count != 1 {
		t.Errorf("books_indexed = %d after three updates, want 1", count)
	}
	ctx := context.Background()
	var document struct {
		Term     string           `bson:"_id"`
		Postings []map[string]any `bson:"postings"`
	}
	if err := client.Database().Collection(mongodb.InvertedIndexCollection).
		FindOne(ctx, map[string]any{"_id": "curiosity"}).Decode(&document); err != nil {
		t.Fatalf("fetch postings of a known term: %v", err)
	}
	if len(document.Postings) != 1 {
		t.Errorf("postings of a single indexed book hold %d entries, want 1", len(document.Postings))
	}
}

func TestIndexingServiceKeepsDataAcrossInstances(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t, sampleBook(7, "Sample", "curiosity"))

	if err := newWiredService(t, lake, client).IndexBook(context.Background(), 7); err != nil {
		t.Fatalf("index book: %v", err)
	}
	restarted := newWiredService(t, lake, client)
	if count := restarted.Stats().BooksIndexed; count != 1 {
		t.Errorf("books_indexed after restart = %d, want 1", count)
	}
}

func TestIndexingServiceMissingBookFails(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t)
	index := newWiredService(t, lake, client)

	err := index.IndexBook(context.Background(), 999)
	if !errors.Is(err, models.ErrBookNotFound) {
		t.Fatalf("IndexBook of a missing book = %v, want models.ErrBookNotFound", err)
	}
	if count := index.Stats().BooksIndexed; count != 0 {
		t.Errorf("books_indexed = %d after a failed update, want 0", count)
	}
}

func TestRebuildPublishesEveryBookOfTheDatalake(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t,
		sampleBook(1, "First", "alpha"),
		sampleBook(2, "Second", "beta"),
	)
	index := newWiredService(t, lake, client)

	report, err := index.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.BooksProcessed != 2 {
		t.Errorf("books_processed = %d, want 2", report.BooksProcessed)
	}
	if report.Elapsed <= 0 {
		t.Error("elapsed is not positive")
	}
	if count := index.Stats().BooksIndexed; count != 2 {
		t.Errorf("books_indexed = %d, want 2", count)
	}
}

func TestRebuildReplacesPreviousContent(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t, sampleBook(1, "Only", "alpha"))
	index := newWiredService(t, lake, client)
	if err := index.IndexBook(context.Background(), 1); err != nil {
		t.Fatalf("index book: %v", err)
	}
	if err := os.RemoveAll(lake.BookDirectory(1)); err != nil {
		t.Fatalf("remove book from datalake: %v", err)
	}

	report, err := index.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.BooksProcessed != 0 {
		t.Errorf("books_processed = %d, want 0", report.BooksProcessed)
	}
	if count := index.Stats().BooksIndexed; count != 0 {
		t.Errorf("books_indexed = %d after rebuilding an empty datalake, want 0", count)
	}
	if publishedCount(t, client) != 0 {
		t.Errorf("published books in datamart = %d, want 0", publishedCount(t, client))
	}
}

func TestRebuildRejectsWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	client := datamartTestStart(t)
	lake := storeBooks(t, sampleBook(1, "Sample", "alpha"))
	index := newWiredService(t, lake, client)
	lock, err := client.AcquireRebuildLock(context.Background())
	if err != nil {
		t.Fatalf("acquire the datamart rebuild lock: %v", err)
	}

	if _, err := index.Rebuild(context.Background()); !errors.Is(err, indexing.ErrRebuildInProgress) {
		t.Fatalf("Rebuild while another replica holds the lease = %v, want indexing.ErrRebuildInProgress", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("release the datamart rebuild lock: %v", err)
	}
	if _, err := index.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild after the lease was released: %v", err)
	}
}
