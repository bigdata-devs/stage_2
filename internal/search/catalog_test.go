package search_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type fakeCatalogStore struct {
	book   models.BookMetadata
	found  bool
	values models.FilterValues
	err    error
}

func (store fakeCatalogStore) PublishedBook(context.Context, models.BookID) (models.BookMetadata, bool, error) {
	return store.book, store.found, store.err
}

func (store fakeCatalogStore) FilterOptions(context.Context) (models.FilterValues, error) {
	return store.values, store.err
}

func newCatalog(t *testing.T, store search.CatalogStore) *search.CatalogService {
	t.Helper()
	catalog, err := search.NewCatalogService(store)
	if err != nil {
		t.Fatalf("NewCatalogService: %v", err)
	}
	return catalog
}

func TestNewCatalogServiceRejectsMissingStore(t *testing.T) {
	if _, err := search.NewCatalogService(nil); !errors.Is(err, search.ErrMissingCatalogStore) {
		t.Errorf("NewCatalogService(nil) = %v, want ErrMissingCatalogStore", err)
	}
}

func TestCatalogReportsThePublishedBookOfTheStore(t *testing.T) {
	book := models.BookMetadata{BookID: 1342, Title: "Pride and Prejudice"}
	catalog := newCatalog(t, fakeCatalogStore{book: book, found: true})

	got, found, err := catalog.PublishedBook(context.Background(), 1342)

	if err != nil || !found || got != book {
		t.Errorf("PublishedBook = %+v, %t, %v; want %+v, true, nil", got, found, err, book)
	}
}

func TestCatalogReportsTheFilterValuesOfTheStore(t *testing.T) {
	values := models.FilterValues{Languages: []string{"en", "fr"}, Years: []int{1813}}
	catalog := newCatalog(t, fakeCatalogStore{values: values})

	got, err := catalog.FilterOptions(context.Background())

	if err != nil || !reflect.DeepEqual(got, values) {
		t.Errorf("FilterOptions = %+v, %v; want %+v, nil", got, err, values)
	}
}

func TestCatalogPropagatesStoreFailures(t *testing.T) {
	failure := errors.New("datamart unreachable")
	catalog := newCatalog(t, fakeCatalogStore{err: failure})

	if _, _, err := catalog.PublishedBook(context.Background(), 1); !errors.Is(err, failure) {
		t.Errorf("PublishedBook error = %v, want the store failure", err)
	}
	if _, err := catalog.FilterOptions(context.Background()); !errors.Is(err, failure) {
		t.Errorf("FilterOptions error = %v, want the store failure", err)
	}
}
