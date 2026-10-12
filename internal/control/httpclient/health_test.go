package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestPingAcceptsAny2xxAnswer(t *testing.T) {
	url := serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Errorf("expected path /health, got %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"status":"running"}`)
	})
	if err := NewIngestionClient(url, testTimeout).Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPingClassifiesUnhealthyAnswer(t *testing.T) {
	url := serveStatus(t, http.StatusServiceUnavailable, "")
	if err := NewIndexingClient(url, testTimeout).Ping(context.Background()); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient, got %v", err)
	}
}

func TestPingTreatsNetworkFailureAsTransient(t *testing.T) {
	client := NewIngestionClient("http://127.0.0.1:1", testTimeout)
	if err := client.Ping(context.Background()); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient, got %v", err)
	}
}
