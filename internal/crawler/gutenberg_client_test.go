package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"boogle-engine/internal/models"
)

func serveStatus(t *testing.T, statusCode int, body string) GutenbergClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(statusCode)
		fmt.Fprint(writer, body)
	}))
	t.Cleanup(server.Close)
	return NewGutenbergClient(server.URL, 5*time.Second)
}

func TestBookURLFollowsGutenbergCacheLayout(t *testing.T) {
	client := NewGutenbergClient(DefaultBaseURL, time.Second)
	expected := "https://www.gutenberg.org/cache/epub/1342/pg1342.txt"
	if url := client.BookURL(1342); url != expected {
		t.Fatalf("expected %s, got %s", expected, url)
	}
}

func TestFetchBookReturnsSplitBook(t *testing.T) {
	book, err := serveStatus(t, http.StatusOK, sampleText).FetchBook(context.Background(), 1342)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book.ID != 1342 || book.Header != sampleHeader {
		t.Fatalf("unexpected book %+v", book)
	}
}

func TestFetchBookClassifiesClientErrorsAsUnavailable(t *testing.T) {
	for _, statusCode := range []int{400, 403, 404, 410} {
		_, err := serveStatus(t, statusCode, "").FetchBook(context.Background(), 1)
		if !errors.Is(err, models.ErrBookUnavailable) {
			t.Errorf("HTTP %d: expected models.ErrBookUnavailable, got %v", statusCode, err)
		}
	}
}

func TestFetchBookClassifiesThrottlingAndServerErrorsAsTransient(t *testing.T) {
	for _, statusCode := range []int{408, 429, 500, 502, 503} {
		_, err := serveStatus(t, statusCode, "").FetchBook(context.Background(), 1)
		if !errors.Is(err, models.ErrTransient) {
			t.Errorf("HTTP %d: expected models.ErrTransient, got %v", statusCode, err)
		}
	}
}

func TestFetchBookWithoutMarkersIsUnavailable(t *testing.T) {
	_, err := serveStatus(t, http.StatusOK, "Just some text").FetchBook(context.Background(), 1)
	if !errors.Is(err, models.ErrBookUnavailable) {
		t.Fatalf("expected models.ErrBookUnavailable, got %v", err)
	}
}

func TestFetchBookNetworkErrorIsTransient(t *testing.T) {
	client := NewGutenbergClient("http://127.0.0.1:1", time.Second)
	if _, err := client.FetchBook(context.Background(), 1); !errors.Is(err, models.ErrTransient) {
		t.Fatalf("expected models.ErrTransient, got %v", err)
	}
}
