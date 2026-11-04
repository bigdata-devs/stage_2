package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

type IndexStatus struct {
	BooksIndexed int     `json:"books_indexed"`
	LastUpdate   string  `json:"last_update"`
	IndexSizeMB  float64 `json:"index_size_MB"`
}

type indexUpdateAnswer struct {
	BookID models.BookID `json:"book_id"`
	Index  string        `json:"index"`
}

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

func (client IndexingClient) Update(ctx context.Context, bookID models.BookID) error {
	var answer indexUpdateAnswer
	return doJSON(ctx, client.httpClient, http.MethodPost, client.updateURL(bookID), &answer)
}

func (client IndexingClient) Stats(ctx context.Context) (IndexStatus, error) {
	var stats IndexStatus
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statsURL(), &stats); err != nil {
		return IndexStatus{}, err
	}
	return stats, nil
}

func (client IndexingClient) updateURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/index/update/%s", client.baseURL, bookID.String())
}

func (client IndexingClient) statsURL() string {
	return client.baseURL + "/index/status"
}
