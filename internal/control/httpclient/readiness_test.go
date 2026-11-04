package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"boogle-engine/internal/models"
)

func TestIngestionReadyListsTheDatalake(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/ingest/list" {
			t.Errorf("request = %s %s, want GET /ingest/list", request.Method, request.URL.Path)
		}
		fmt.Fprint(writer, `{"count":0,"books":[]}`)
	})

	if err := NewIngestionClient(url, testTimeout).Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestIndexingReadyReadsTheIndexStatus(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/index/status" {
			t.Errorf("request = %s %s, want GET /index/status", request.Method, request.URL.Path)
		}
		fmt.Fprint(writer, `{"books_indexed":0,"last_update":"2026-11-04T20:00:00Z","index_size_MB":0}`)
	})

	if err := NewIndexingClient(url, testTimeout).Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestReadyReportsAnUnavailableServiceAsTransient(t *testing.T) {
	url := serveStatus(t, http.StatusServiceUnavailable, "")

	if err := NewIngestionClient(url, testTimeout).Ready(context.Background()); !errors.Is(err, models.ErrTransient) {
		t.Errorf("ingestion Ready = %v, want models.ErrTransient", err)
	}
	if err := NewIndexingClient(url, testTimeout).Ready(context.Background()); !errors.Is(err, models.ErrTransient) {
		t.Errorf("indexing Ready = %v, want models.ErrTransient", err)
	}
}

func TestListDatalakeBooksReturnsTheCompleteBooks(t *testing.T) {
	url := serveStatus(t, http.StatusOK, `{"count":3,"books":[5,17,1342]}`)

	books, err := NewIngestionClient(url, testTimeout).ListDatalakeBooks(context.Background())

	if err != nil || !slices.Equal(books, []models.BookID{5, 17, 1342}) {
		t.Fatalf("ListDatalakeBooks = %v, %v; want [5 17 1342]", books, err)
	}
}
