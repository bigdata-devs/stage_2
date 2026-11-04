package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"boogle-engine/internal/models"
)

func TestUpdateReportsIndexedBook(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", request.Method)
		}
		if request.URL.Path != "/index/update/1342" {
			t.Errorf("expected path /index/update/1342, got %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"book_id":1342,"index":"updated"}`)
	})
	result, err := NewIndexingClient(url, testTimeout).Update(context.Background(), 1342)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BookID != 1342 || result.Index != "updated" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestUpdateClassifiesHTTPAnswers(t *testing.T) {
	for _, testCase := range []struct {
		statusCode int
		want       error
	}{
		{http.StatusNotFound, models.ErrPermanent},
		{http.StatusBadRequest, models.ErrPermanent},
		{http.StatusTooManyRequests, models.ErrTransient},
		{http.StatusInternalServerError, models.ErrTransient},
	} {
		url := serveStatus(t, testCase.statusCode, "")
		_, err := NewIndexingClient(url, testTimeout).Update(context.Background(), 1)
		if !errors.Is(err, testCase.want) {
			t.Errorf("HTTP %d: expected %v, got %v", testCase.statusCode, testCase.want, err)
		}
	}
}

func TestStatsDecodesProgress(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/index/status" {
			t.Errorf("expected path /index/status, got %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"books_indexed":1200,"last_update":"2025-10-08T14:05:00Z","index_size_MB":42.7}`)
	})
	stats, err := NewIndexingClient(url, testTimeout).Stats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.BooksIndexed != 1200 || stats.IndexSizeMB != 42.7 {
		t.Fatalf("unexpected stats %+v", stats)
	}
}
