package mongodb

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type fakeBooksSearcher struct {
	result   SearchResult
	err      error
	calls    int
	received SearchRequest
}

func (searcher *fakeBooksSearcher) SearchBooks(_ context.Context, request SearchRequest) (SearchResult, error) {
	searcher.calls++
	searcher.received = request
	return searcher.result, searcher.err
}

func TestSearchIndexMapsTheQueryToADatamartRequest(t *testing.T) {
	searcher := &fakeBooksSearcher{result: SearchResult{Books: []models.BookMetadata{}}}
	query := search.Query{
		Text:    "Sherlock Holmes!",
		Terms:   []string{"sherlock", "holmes"},
		Filters: search.Filters{Author: "Doyle", Language: "en", Year: 1892},
		Offset:  20,
		Limit:   10,
	}

	if _, err := (SearchIndex{searcher: searcher}).Search(context.Background(), query); err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := SearchRequest{Terms: []string{"sherlock", "holmes"}, Author: "Doyle", Language: "en", Year: 1892, Offset: 20, Limit: 10}
	if searcher.calls != 1 || !reflect.DeepEqual(searcher.received, want) {
		t.Errorf("datamart got %d calls with %+v, want 1 call with %+v", searcher.calls, searcher.received, want)
	}
}

func TestSearchIndexReturnsTheDatamartPage(t *testing.T) {
	books := []models.BookMetadata{
		{BookID: 2, Title: "More Adventures", Author: "jane austin", Language: "en,fr", PublicationYear: 1850},
		{BookID: 1, Title: "Island Adventure", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
	}
	searcher := &fakeBooksSearcher{result: SearchResult{Total: 7, Books: books}}

	page, err := (SearchIndex{searcher: searcher}).Search(context.Background(), search.Query{Terms: []string{"adventure"}, Limit: 2})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.Total != 7 || !slices.Equal(page.Books, books) {
		t.Errorf("page = %+v, want total 7 and the datamart books in datamart order", page)
	}
}

func TestSearchIndexPropagatesADatamartFailure(t *testing.T) {
	failure := errors.New("server selection timeout")
	searcher := &fakeBooksSearcher{err: failure}

	_, err := (SearchIndex{searcher: searcher}).Search(context.Background(), search.Query{Terms: []string{"whale"}, Limit: 1})

	if !errors.Is(err, failure) {
		t.Errorf("Search = %v, want the datamart failure", err)
	}
}
