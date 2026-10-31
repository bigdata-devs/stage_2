package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/api"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// searchDatamartStart connects to a live datamart dedicated to these tests. It skips when MongoDB
// is not reachable and drops the collections it is about to pollute. The database name is
// overridden so the suite never collides with the internal/mongodb or cmd/indexing tests, which
// the go test command may run at the same time.
func searchDatamartStart(t *testing.T) mongodb.Client {
	t.Helper()
	t.Setenv("MONGO_DATABASE", "boogle_search_bridge_test")
	config := mongodb.ConfigFromEnvironment()
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	if !mongodb.Available(ctx, config) {
		t.Skipf("MongoDB is not available at %s; start it with: docker run -d --name boogle-mongo -p 27017:27017 mongo:7", mongodb.RedactURI(config.URI))
	}
	client, err := mongodb.Connect(ctx, config, mongodb.DefaultConnectTimeout)
	if err != nil {
		t.Fatalf("connect to datamart: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Database().Drop(context.Background()); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("close datamart: %v", err)
		}
	})
	if err := client.Database().Drop(ctx); err != nil {
		t.Fatalf("drop test database: %v", err)
	}
	if err := client.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return client
}

// indexBody writes one book through the Indexing Service's real write path, tokenizing the body
// exactly as the service does.
func indexBody(t *testing.T, client mongodb.Client, metadata models.BookMetadata, body string) {
	t.Helper()
	input := models.IndexBookInput{Metadata: metadata, Tokens: indexer.ProcessText(body)}
	if err := mongodb.NewBookIndexer(client).IndexBook(context.Background(), input); err != nil {
		t.Fatalf("index book %d: %v", metadata.BookID, err)
	}
}

// markPending reverts a published book to the in-progress state an interrupted indexing leaves.
func markPending(t *testing.T, client mongodb.Client, bookID models.BookID) {
	t.Helper()
	_, err := client.Database().Collection(mongodb.BooksCollection).UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: bookID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: mongodb.StatusPending}}}})
	if err != nil {
		t.Fatalf("mark book %d pending: %v", bookID, err)
	}
}

func pageIDs(page api.SearchPage) []models.BookID {
	ids := make([]models.BookID, 0, len(page.Books))
	for _, book := range page.Books {
		ids = append(ids, book.BookID)
	}
	return ids
}

// TestDatamartEngineAgainstLiveDatamart seeds the datamart through the indexing write path and
// checks the bridge end to end. The subtests share one connection, so a machine without MongoDB
// pays the connection timeout once.
func TestDatamartEngineAgainstLiveDatamart(t *testing.T) {
	client := searchDatamartStart(t)
	indexBody(t, client,
		models.BookMetadata{BookID: 1, Title: "Island Adventure", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
		"An adventure on the island. The adventure ends.")
	indexBody(t, client,
		models.BookMetadata{BookID: 2, Title: "More Adventures", Author: "jane austin", Language: "en,fr", PublicationYear: 1850},
		"adventure adventure adventure adventure adventure")
	indexBody(t, client,
		models.BookMetadata{BookID: 3, Title: "Unfinished Adventure", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
		"adventure adventure adventure adventure adventure adventure adventure adventure adventure")
	markPending(t, client, 3)
	engine := NewDatamartEngine(mongodb.NewDatamartReader(client))
	ctx := context.Background()

	t.Run("raw keyword is normalized and ranked by term frequency", func(t *testing.T) {
		page, err := engine.Search(ctx, api.SearchQuery{Term: "Adventure!", Limit: api.DefaultLimit})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if want := []models.BookID{2, 1}; page.Total != 2 || !slices.Equal(pageIDs(page), want) {
			t.Errorf("total = %d, IDs = %v, want 2 published matches %v (pending book 3 hidden)", page.Total, pageIDs(page), want)
		}
	})

	t.Run("filters combine with AND", func(t *testing.T) {
		page, err := engine.Search(ctx, api.SearchQuery{
			Term: "adventure", Filters: api.SearchFilters{Author: "austen", Language: "en", Year: 1813}, Limit: api.DefaultLimit,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if want := []models.BookID{1}; page.Total != 1 || !slices.Equal(pageIDs(page), want) {
			t.Errorf("total = %d, IDs = %v, want %v", page.Total, pageIDs(page), want)
		}
	})

	t.Run("total counts every page", func(t *testing.T) {
		page, err := engine.Search(ctx, api.SearchQuery{Term: "adventure", Offset: 1, Limit: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if want := []models.BookID{1}; page.Total != 2 || !slices.Equal(pageIDs(page), want) {
			t.Errorf("total = %d, IDs = %v, want total 2 and second page %v", page.Total, pageIDs(page), want)
		}
	})

	t.Run("GET /search serves the datamart through the production router", func(t *testing.T) {
		router := newTestRouter(t, mongodb.NewDatamartReader(client))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=adventure&language=FR", nil))

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		want := `{"query":"adventure","filters":{"language":"fr"},"total":1,"offset":0,"limit":20,` +
			`"results":[{"book_id":2,"title":"More Adventures","author":"jane austin","language":"en,fr","year":1850}]}`
		if body := trimNewline(recorder.Body.String()); body != want {
			t.Errorf("body = %s, want %s", body, want)
		}
	})
}
