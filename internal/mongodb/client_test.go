package mongodb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// These tests exercise a live server. TestMain probes availability once for the whole package,
// so the suite stays green and skips fast on machines without MongoDB (the same policy as the
// Stage 1 Python tests).

var (
	liveMongoConfig    Config
	liveMongoAvailable bool
)

func TestMain(m *testing.M) {
	liveMongoConfig = ConfigFromEnvironment()
	liveMongoAvailable = Available(context.Background(), liveMongoConfig)
	if !liveMongoAvailable {
		fmt.Fprintf(os.Stderr, "no MongoDB reachable at %s; the live tests of this package will skip (set MONGO_URI or start a server)\n", RedactURI(liveMongoConfig.URI))
	}
	os.Exit(m.Run())
}

func liveConfig(t testing.TB) Config {
	t.Helper()
	if !liveMongoAvailable {
		t.Skipf("no MongoDB reachable at %s; set MONGO_URI or start a server", RedactURI(liveMongoConfig.URI))
	}
	return liveMongoConfig
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
