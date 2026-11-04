package search

import (
	"context"
	"errors"

	"boogle-engine/internal/models"
)

var ErrMissingCatalogStore = errors.New("catalog service needs a catalog store")

var _ BookCatalog = (*CatalogService)(nil)

type CatalogService struct {
	store CatalogStore
}

func NewCatalogService(store CatalogStore) (*CatalogService, error) {
	if store == nil {
		return nil, ErrMissingCatalogStore
	}
	return &CatalogService{store: store}, nil
}

func (catalog *CatalogService) PublishedBook(ctx context.Context, bookID models.BookID) (models.BookMetadata, bool, error) {
	return catalog.store.PublishedBook(ctx, bookID)
}

func (catalog *CatalogService) FilterOptions(ctx context.Context) (models.FilterValues, error) {
	return catalog.store.FilterOptions(ctx)
}
