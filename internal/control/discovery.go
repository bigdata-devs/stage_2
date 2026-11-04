package control

import (
	"context"
	"fmt"
	"log/slog"

	"boogle-engine/internal/models"
)

type Discoverer struct {
	datalake DatalakeBookLister
}

func NewDiscoverer(datalake DatalakeBookLister) (Discoverer, error) {
	if datalake == nil {
		return Discoverer{}, ErrMissingDependency
	}
	return Discoverer{datalake: datalake}, nil
}

func (discoverer Discoverer) Discover(ctx context.Context, candidates []models.BookID) ([]models.BookID, error) {
	stored, err := discoverer.datalake.ListDatalakeBooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover pending books: %w", err)
	}
	slog.Info("[CONTROL] discovered complete books", "count", len(stored))
	discovered := make([]models.BookID, 0, len(candidates)+len(stored))
	discovered = append(discovered, candidates...)
	return append(discovered, stored...), nil
}
