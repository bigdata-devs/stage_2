package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

// IngestionClient calls the internal API of the Ingestion Service.
// A single client is shared by every orchestrator worker: http.Client is safe
// for concurrent use and reuses its TCP connections.
type IngestionClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewIngestionClient returns a client for the Ingestion Service at baseURL
// with a per-call timeout covering the whole request and answer.
func NewIngestionClient(baseURL string, requestTimeout time.Duration) IngestionClient {
	return IngestionClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Ingest asks the Ingestion Service to download a book into the datalake and waits
// for its final answer. A 2xx status means the work has completed: the returned
// result reports downloaded, available or unavailable. Transport and HTTP failures
// wrap ErrTransient or ErrPermanent.
func (client IngestionClient) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	var result models.IngestionResult
	if err := doJSON(ctx, client.httpClient, http.MethodPost, client.bookURL(bookID), &result); err != nil {
		return models.IngestionResult{}, err
	}
	return result, nil
}

// Status asks the Ingestion Service whether a book already lives in the datalake.
func (client IngestionClient) Status(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	var result models.IngestionResult
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statusURL(bookID), &result); err != nil {
		return models.IngestionResult{}, err
	}
	return result, nil
}

// Ping checks that the Ingestion Service answers its health endpoint.
func (client IngestionClient) Ping(ctx context.Context) error {
	return pingService(ctx, client.httpClient, client.baseURL)
}

func (client IngestionClient) bookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/%s", client.baseURL, bookID.String())
}

func (client IngestionClient) statusURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/status/%s", client.baseURL, bookID.String())
}
