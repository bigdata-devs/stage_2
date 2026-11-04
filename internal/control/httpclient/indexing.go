package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

type IndexingClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewIndexingClient(baseURL string, requestTimeout time.Duration) IndexingClient {
	return IndexingClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

func (client IndexingClient) Update(ctx context.Context, bookID models.BookID) (models.IndexUpdateResult, error) {
	var result models.IndexUpdateResult
	if err := doJSON(ctx, client.httpClient, http.MethodPost, client.updateURL(bookID), &result); err != nil {
		return models.IndexUpdateResult{}, err
	}
	return result, nil
}

func (client IndexingClient) Stats(ctx context.Context) (models.IndexStats, error) {
	var stats models.IndexStats
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statsURL(), &stats); err != nil {
		return models.IndexStats{}, err
	}
	return stats, nil
}

func (client IndexingClient) updateURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/index/update/%s", client.baseURL, bookID.String())
}

func (client IndexingClient) statsURL() string {
	return client.baseURL + "/index/status"
}
