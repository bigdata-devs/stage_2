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
	// DefaultBaseURL is the official Project Gutenberg plain-text cache.
	DefaultBaseURL = "https://www.gutenberg.org/cache/epub"
	// DefaultRequestTimeout bounds a whole download, body included. Python's 10 s timeout only
	// limited the wait between bytes, so a longer total budget is used for large books.
	DefaultRequestTimeout = 30 * time.Second
	userAgent             = "boogle-engine/2.0 (ULPGC Big Data course project)"
)

// GutenbergClient downloads books over HTTP. A single client is shared by every worker:
// http.Client is safe for concurrent use and reuses its TCP connections.
type GutenbergClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewGutenbergClient returns a client for the mirror at baseURL with a per-download timeout.
func NewGutenbergClient(baseURL string, requestTimeout time.Duration) GutenbergClient {
	return GutenbergClient{baseURL: baseURL, httpClient: &http.Client{Timeout: requestTimeout}}
}

// BookURL returns the plain-text URL of a book: "<base>/<id>/pg<id>.txt".
func (client GutenbergClient) BookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/%d/pg%d.txt", client.baseURL, bookID, bookID)
}

// FetchBook ports fetch_and_split: it downloads a book and splits it into header and body.
// Errors wrap ErrBookUnavailable or ErrTransientDownload so callers can decide whether to retry.
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

// classifyStatus ports _raise_for_failed_status: 200 is success, most 4xx codes are permanent
// and everything else (408, 429, 5xx...) is transient.
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
