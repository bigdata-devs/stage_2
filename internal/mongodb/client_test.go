package mongodb

import (
	"context"
	"testing"
	"time"
)

// These tests exercise a live server. They skip automatically when none is reachable, so the
// suite stays green on machines without MongoDB (the same policy as the Stage 1 Python tests).

func liveConfig(t *testing.T) Config {
	t.Helper()
	config := ConfigFromEnvironment()
	if !Available(context.Background(), config) {
		t.Skipf("no MongoDB reachable at %s; set MONGO_URI or start a server", config.URI)
	}
	return config
}

func TestConnectAndCloseRoundTrip(t *testing.T) {
	config := liveConfig(t)
	client, err := Connect(context.Background(), config, DefaultConnectTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := client.Close(context.Background()); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	}()

	if err := client.Ping(context.Background(), DefaultConnectTimeout); err != nil {
		t.Fatalf("unexpected ping error: %v", err)
	}
	if name := client.Database().Name(); name != config.Database {
		t.Fatalf("expected database %q, got %q", config.Database, name)
	}
}

func TestEnsureIndexesIsIdempotent(t *testing.T) {
	config := liveConfig(t)
	client, err := Connect(context.Background(), config, DefaultConnectTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := client.Close(context.Background()); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	}()

	for run := 1; run <= 2; run++ {
		if err := client.EnsureIndexes(context.Background()); err != nil {
			t.Fatalf("run %d: unexpected error: %v", run, err)
		}
	}

	count, err := countBookIndexes(t, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != len(bookFilterIndexes) {
		t.Fatalf("expected %d book indexes, got %d", len(bookFilterIndexes), count)
	}
}

func countBookIndexes(t *testing.T, client Client) (int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	names, err := client.Database().Collection(BooksCollection).Indexes().List(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	for names.Next(ctx) {
		var document struct {
			Name string `bson:"name"`
		}
		if err := names.Decode(&document); err != nil {
			return 0, err
		}
		if document.Name != "_id_" {
			count++
		}
	}
	return count, names.Err()
}
