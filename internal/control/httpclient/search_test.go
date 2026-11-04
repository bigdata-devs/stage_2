package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"boogle-engine/internal/models"
)

func TestIsPublishedReadsTheBookEndpoint(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/books/1342" {
			t.Errorf("request = %s %s, want GET /books/1342", request.Method, request.URL.Path)
		}
		fmt.Fprint(writer, `{"book_id":1342,"title":"Pride and Prejudice"}`)
	})

	published, err := NewSearchClient(url+"/", testTimeout).IsPublished(context.Background(), 1342)

	if err != nil || !published {
		t.Fatalf("IsPublished = %t, %v; want true, nil", published, err)
	}
}

func TestIsPublishedReportsAnUnknownBookAsNotPublished(t *testing.T) {
	url := serveStatus(t, http.StatusNotFound, `{"error":"book_not_found"}`)

	published, err := NewSearchClient(url, testTimeout).IsPublished(context.Background(), 84)

	if err != nil || published {
		t.Fatalf("IsPublished = %t, %v; want false, nil", published, err)
	}
}

func TestIsPublishedClassifiesOtherAnswers(t *testing.T) {
	for _, testCase := range []struct {
		statusCode int
		want       error
	}{
		{http.StatusInternalServerError, models.ErrTransient},
		{http.StatusServiceUnavailable, models.ErrTransient},
		{http.StatusBadRequest, models.ErrPermanent},
	} {
		url := serveStatus(t, testCase.statusCode, "")
		if _, err := NewSearchClient(url, testTimeout).IsPublished(context.Background(), 1); !errors.Is(err, testCase.want) {
			t.Errorf("HTTP %d: expected %v, got %v", testCase.statusCode, testCase.want, err)
		}
	}
}

func TestIsPublishedReportsAnUnreachableServiceAsTransient(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	if _, err := NewSearchClient(url, testTimeout).IsPublished(context.Background(), 1); !errors.Is(err, models.ErrTransient) {
		t.Fatalf("expected models.ErrTransient, got %v", err)
	}
}
