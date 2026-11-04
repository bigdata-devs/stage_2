package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boogle-engine/internal/models"
)

type BookList struct {
	Count int             `json:"count"`
	Books []models.BookID `json:"books"`
}

type ingestionAnswer struct {
	BookID models.BookID          `json:"book_id"`
	Status models.IngestionStatus `json:"status"`
	Path   string                 `json:"path"`
}

func (answer ingestionAnswer) result() models.IngestionResult {
	return models.IngestionResult{BookID: answer.BookID, Status: answer.Status, Path: answer.Path}
}

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
	var answer ingestionAnswer
	if err := doJSON(ctx, client.httpClient, http.MethodPost, client.bookURL(bookID), &answer); err != nil {
		return models.IngestionResult{}, err
	}
	return answer.result(), nil
}

func (client IngestionClient) Status(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	var answer ingestionAnswer
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.statusURL(bookID), &answer); err != nil {
		return models.IngestionResult{}, err
	}
	return answer.result(), nil
}

func (client IngestionClient) List(ctx context.Context) (BookList, error) {
	var list BookList
	if err := doJSON(ctx, client.httpClient, http.MethodGet, client.baseURL+"/ingest/list", &list); err != nil {
		return BookList{}, err
	}
	return list, nil
}

func (client IngestionClient) Ready(ctx context.Context) error {
	_, err := client.List(ctx)
	return err
}

func (client IngestionClient) ListDatalakeBooks(ctx context.Context) ([]models.BookID, error) {
	list, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	return list.Books, nil
}

func (client IngestionClient) bookURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/%s", client.baseURL, bookID.String())
}

func (client IngestionClient) statusURL(bookID models.BookID) string {
	return fmt.Sprintf("%s/ingest/status/%s", client.baseURL, bookID.String())
}
