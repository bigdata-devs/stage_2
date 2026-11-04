package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

type IngestionClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewIngestionClient(baseURL string, requestTimeout time.Duration) IngestionClient {
	return IngestionClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

func (client IngestionClient) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	var result models.IngestionResult
	if err := doJSON(ctx, client.httpClient, http.MethodPost, client.bookURL(bookID), &result); err != nil {
		return models.IngestionResult{}, err
	}
	return result, nil
}

func (client IngestionClient) Status(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	var result models.IngestionResult
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statusURL(bookID), &result); err != nil {
		return models.IngestionResult{}, err
	}
	return result, nil
}

func (client IngestionClient) List(ctx context.Context) (models.IngestionList, error) {
	var list models.IngestionList
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.baseURL+"/ingest/list", &list); err != nil {
		return models.IngestionList{}, err
	}
	return list, nil
}

func (client IngestionClient) bookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/%s", client.baseURL, bookID.String())
}

func (client IngestionClient) statusURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/status/%s", client.baseURL, bookID.String())
}
