package search_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type fakeCatalogStore struct {
	book   models.BookMetadata
	found  bool
	stored search.StoredFilterValues
	err    error
}

func (store fakeCatalogStore) PublishedBook(context.Context, models.BookID) (models.BookMetadata, bool, error) {
	return store.book, store.found, store.err
}

func (store fakeCatalogStore) DistinctFilterValues(context.Context) (search.StoredFilterValues, error) {
	return store.stored, store.err
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

func TestFilterOptionsListsOnlyTheValuesASearchCanMatch(t *testing.T) {
	stored := search.StoredFilterValues{
		LanguageLists: []string{"fr", "en,fr", " de , en ", "Unknown", "", "en,Unknown"},
		Years:         []int{1850, 0, 1813, 1850, search.MaxYear + 1, -5},
	}
	catalog := newCatalog(t, fakeCatalogStore{stored: stored})

	got, err := catalog.FilterOptions(context.Background())

	if err != nil {
		t.Fatalf("FilterOptions: %v", err)
	}
	if want := []string{"de", "en", "fr"}; !slices.Equal(got.Languages, want) {
		t.Errorf("languages = %q, want %q (codes flattened, de-duplicated and sorted, without Unknown)", got.Languages, want)
	}
	if want := []int{1813, 1850}; !slices.Equal(got.Years, want) {
		t.Errorf("years = %v, want %v (only years GET /search accepts, de-duplicated and sorted)", got.Years, want)
	}
}

func TestFilterOptionsOfAnEmptyCatalogAreEmptyLists(t *testing.T) {
	got, err := newCatalog(t, fakeCatalogStore{}).FilterOptions(context.Background())

	if err != nil {
		t.Fatalf("FilterOptions: %v", err)
	}
	if got.Languages == nil || got.Years == nil || len(got.Languages) != 0 || len(got.Years) != 0 {
		t.Errorf("FilterOptions = %+v, want empty non-nil lists", got)
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
