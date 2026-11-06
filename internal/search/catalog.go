package search

import (
	"context"
	"errors"
	"slices"
	"strings"

	"boogle-engine/internal/indexer"
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
	stored, err := catalog.store.DistinctFilterValues(ctx)
	if err != nil {
		return models.FilterValues{}, err
	}
	return models.FilterValues{
		Languages: selectableLanguages(stored.LanguageLists),
		Years:     selectableYears(stored.Years),
	}, nil
}

func selectableLanguages(languageLists []string) []string {
	seen := map[string]bool{}
	codes := []string{}
	for _, list := range languageLists {
		for _, code := range strings.Split(list, ",") {
			code = strings.TrimSpace(code)
			if code == "" || code == indexer.UnknownValue || seen[code] {
				continue
			}
			seen[code] = true
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	return codes
}

func selectableYears(storedYears []int) []int {
	seen := map[int]bool{}
	years := []int{}
	for _, year := range storedYears {
		if year < MinYear || year > MaxYear || seen[year] {
			continue
		}
		seen[year] = true
		years = append(years, year)
	}
	slices.Sort(years)
	return years
}
