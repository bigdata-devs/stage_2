package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"boogle-engine/internal/models"
)

const (
	DefaultBaseURL        = "https://www.gutenberg.org/cache/epub"
	DefaultRequestTimeout = 30 * time.Second
	userAgent             = "boogle-engine/2.0 (ULPGC Big Data course project)"
)

type GutenbergClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewGutenbergClient(baseURL string, requestTimeout time.Duration) GutenbergClient {
	return GutenbergClient{baseURL: baseURL, httpClient: &http.Client{Timeout: requestTimeout}}
}

func (client GutenbergClient) BookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/%d/pg%d.txt", client.baseURL, bookID, bookID)
}

func (client GutenbergClient) FetchBook(ctx context.Context, bookID models.BookID) (models.Book, error) {
	text, err := client.downloadText(ctx, bookID)
	if err != nil {
		return models.Book{}, err
	}
	return SplitBookText(bookID, text)
}

func (client GutenbergClient) downloadText(ctx context.Context, bookID models.BookID) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BookURL(bookID), http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build request for book %d: %w", bookID, err)
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: book %d: %w", ErrTransientDownload, bookID, err)
	}
	defer response.Body.Close()
	if err := classifyStatus(bookID, response.StatusCode); err != nil {
		return "", err
	}
	return readText(bookID, response.Body)
}

func readText(bookID models.BookID, body io.Reader) (string, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("%w: read book %d: %w", ErrTransientDownload, bookID, err)
	}
	return string(content), nil
}

func classifyStatus(bookID models.BookID, statusCode int) error {
	switch {
	case statusCode == http.StatusOK:
		return nil
	case isPermanentClientError(statusCode):
		return fmt.Errorf("%w: book %d (HTTP %d)", ErrBookUnavailable, bookID, statusCode)
	default:
		return fmt.Errorf("%w: book %d (HTTP %d)", ErrTransientDownload, bookID, statusCode)
	}
}

func isPermanentClientError(statusCode int) bool {
	isClientError := statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError
	isRetryable := statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests
	return isClientError && !isRetryable
}
