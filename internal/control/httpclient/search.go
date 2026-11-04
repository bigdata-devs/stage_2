package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

type SearchClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewSearchClient(baseURL string, requestTimeout time.Duration) SearchClient {
	return SearchClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

func (client SearchClient) IsPublished(ctx context.Context, bookID models.BookID) (bool, error) {
	url := client.bookURL(bookID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("%w: build GET %s: %w", models.ErrPermanent, url, err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return false, fmt.Errorf("%w: GET %s: %w", models.ErrTransient, url, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		_, _ = io.Copy(io.Discard, response.Body)
		return true, nil
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, response.Body)
		return false, nil
	default:
		return false, statusFailure(response, http.MethodGet, url)
	}
}

func (client SearchClient) bookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/books/%s", client.baseURL, bookID.String())
}
