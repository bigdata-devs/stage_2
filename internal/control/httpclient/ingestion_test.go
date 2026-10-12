package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"boogle-engine/internal/models"
)

func TestIngestDownloadsBook(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", request.Method)
		}
		if request.URL.Path != "/ingest/1342" {
			t.Errorf("expected path /ingest/1342, got %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"book_id":1342,"status":"downloaded","path":"datalake/batch_1000_1999/1342"}`)
	})
	result, err := NewIngestionClient(url, testTimeout).Ingest(context.Background(), 1342)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != models.StatusDownloaded || result.BookID != 1342 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestIngestReportsAvailableBook(t *testing.T) {
	url := serveStatus(t, http.StatusOK, `{"book_id":84,"status":"available"}`)
	result, err := NewIngestionClient(url, testTimeout).Ingest(context.Background(), 84)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != models.StatusAvailable || result.BookID != 84 {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestIngestClassifiesHTTPAnswers(t *testing.T) {
	for _, statusCode := range []int{400, 401, 404} {
		url := serveStatus(t, statusCode, "")
		_, err := NewIngestionClient(url, testTimeout).Ingest(context.Background(), 1)
		if !errors.Is(err, ErrPermanent) {
			t.Errorf("HTTP %d: expected ErrPermanent, got %v", statusCode, err)
		}
	}
	for _, statusCode := range []int{408, 429, 500, 503} {
		url := serveStatus(t, statusCode, "")
		_, err := NewIngestionClient(url, testTimeout).Ingest(context.Background(), 1)
		if !errors.Is(err, ErrTransient) {
			t.Errorf("HTTP %d: expected ErrTransient, got %v", statusCode, err)
		}
	}
}

func TestIngestTreatsNetworkFailureAsTransient(t *testing.T) {
	client := NewIngestionClient("http://127.0.0.1:1", testTimeout)
	if _, err := client.Ingest(context.Background(), 1); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient, got %v", err)
	}
}

func TestIngestTreatsMalformedAnswerAsTransient(t *testing.T) {
	url := serveStatus(t, http.StatusOK, "not json")
	_, err := NewIngestionClient(url, testTimeout).Ingest(context.Background(), 1)
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient, got %v", err)
	}
}

func TestStatusAsksDedicatedRoute(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", request.Method)
		}
		if request.URL.Path != "/ingest/status/1342" {
			t.Errorf("expected path /ingest/status/1342, got %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"book_id":1342,"status":"available"}`)
	})
	result, err := NewIngestionClient(url, testTimeout).Status(context.Background(), 1342)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != models.StatusAvailable || result.BookID != 1342 {
		t.Fatalf("unexpected result %+v", result)
	}
}
