package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

// IndexingClient calls the internal API of the Indexing Service.
type IndexingClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewIndexingClient returns a client for the Indexing Service at baseURL
// with a per-call timeout covering the whole request and answer.
func NewIndexingClient(baseURL string, requestTimeout time.Duration) IndexingClient {
	return IndexingClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Update asks the Indexing Service to read a book from the datalake and update
// metadata and inverted index. The call is idempotent, so a repeated update of an
// already processed book never creates duplicates. A 2xx status means the update
// has been published and the book is searchable.
func (client IndexingClient) Update(ctx context.Context, bookID models.BookID) (models.IndexUpdateResult, error) {
	var result models.IndexUpdateResult
	if err := doJSON(ctx, client.httpClient, http.MethodPost, client.updateURL(bookID), &result); err != nil {
		return models.IndexUpdateResult{}, err
	}
	return result, nil
}

// Stats fetches the indexing progress reported by GET /index/status.
func (client IndexingClient) Stats(ctx context.Context) (models.IndexStats, error) {
	var stats models.IndexStats
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statsURL(), &stats); err != nil {
		return models.IndexStats{}, err
	}
	return stats, nil
}

// Ping checks that the Indexing Service answers its health endpoint.
func (client IndexingClient) Ping(ctx context.Context) error {
	return pingService(ctx, client.httpClient, client.baseURL)
}

func (client IndexingClient) updateURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/index/update/%s", client.baseURL, bookID.String())
}

func (client IndexingClient) statsURL() string {
	return client.baseURL + "/index/status"
}
