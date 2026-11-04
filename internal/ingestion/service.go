package ingestion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"boogle-engine/internal/models"
)

var ErrMissingPort = errors.New("ingestion service needs a book fetcher and a book store")

var (
	_ BookIngester   = (*Service)(nil)
	_ DatalakeReader = (*Service)(nil)
)

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
}

type Limits struct {
	Timeout                time.Duration
	MaxConcurrentDownloads int
}

type Service struct {
	fetcher  BookFetcher
	store    BookStore
	retry    RetryPolicy
	timeout  time.Duration
	slots    chan struct{}
	mutex    sync.Mutex
	inFlight map[models.BookID]*ingestionCall
}

func NewService(ports Ports, retry RetryPolicy, limits Limits) (*Service, error) {
	if ports.Fetcher == nil || ports.Store == nil {
		return nil, ErrMissingPort
	}
	if retry.MaxAttempts < 1 {
		return nil, fmt.Errorf("max attempts must be a positive integer, got %d", retry.MaxAttempts)
	}
	if retry.InitialBackoff < 0 {
		return nil, fmt.Errorf("initial backoff must not be negative, got %s", retry.InitialBackoff)
	}
	if limits.Timeout <= 0 {
		return nil, fmt.Errorf("ingestion timeout must be positive, got %s", limits.Timeout)
	}
	if limits.MaxConcurrentDownloads < 1 {
		return nil, fmt.Errorf("max concurrent downloads must be a positive integer, got %d", limits.MaxConcurrentDownloads)
	}
	return &Service{
		fetcher:  ports.Fetcher,
		store:    ports.Store,
		retry:    retry,
		timeout:  limits.Timeout,
		slots:    make(chan struct{}, limits.MaxConcurrentDownloads),
		inFlight: map[models.BookID]*ingestionCall{},
	}, nil
}

func (service *Service) Ingest(ctx context.Context, bookID models.BookID) (models.IngestionResult, error) {
	call := service.join(bookID)
	select {
	case <-call.done:
		return call.result, nil
	case <-ctx.Done():
		return models.IngestionResult{}, ctx.Err()
	}
}

func (service *Service) Locate(bookID models.BookID) (models.StoredBook, error) {
	return service.store.Locate(bookID)
}

func (service *Service) ListBookIDs() ([]models.BookID, error) {
	return service.store.ListBookIDs()
}
