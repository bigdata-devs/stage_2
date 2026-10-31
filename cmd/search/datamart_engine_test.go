package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"boogle-engine/internal/api"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// fakeSearcher stands in for mongodb.DatamartReader: it records the last request the engine sent
// and answers with a fixed result or error, so these tests need no MongoDB server.
type fakeSearcher struct {
	result   mongodb.SearchResult
	err      error
	calls    int
	received mongodb.SearchRequest
}

func (searcher *fakeSearcher) SearchBooks(_ context.Context, request mongodb.SearchRequest) (mongodb.SearchResult, error) {
	searcher.calls++
	searcher.received = request
	return searcher.result, searcher.err
}

// emptyCatalog satisfies api.BookCatalog so the real router can be built around the engine.
type emptyCatalog struct{}

func (emptyCatalog) PublishedBook(context.Context, models.BookID) (models.BookMetadata, bool, error) {
	return models.BookMetadata{}, false, nil
}

func (emptyCatalog) FilterOptions(context.Context) (models.FilterValues, error) {
	return models.FilterValues{Languages: []string{}, Years: []int{}}, nil
}

func TestDatamartEngineMapsQueryToDatamartRequest(t *testing.T) {
	searcher := &fakeSearcher{result: mongodb.SearchResult{Books: []models.BookMetadata{}}}
	query := api.SearchQuery{
		Term:    "Adventure!",
		Filters: api.SearchFilters{Author: "Austen", Language: "en", Year: 1813},
		Offset:  20,
		Limit:   10,
	}

	if _, err := NewDatamartEngine(searcher).Search(context.Background(), query); err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := mongodb.SearchRequest{Term: "adventure", Author: "Austen", Language: "en", Year: 1813, Offset: 20, Limit: 10}
	if searcher.calls != 1 || searcher.received != want {
		t.Errorf("datamart got %d calls with %+v, want 1 call with %+v", searcher.calls, searcher.received, want)
	}
}

func TestDatamartEngineSearchesFirstIndexableToken(t *testing.T) {
	cases := []struct {
		keyword  string
		wantTerm string
	}{
		{keyword: "whale", wantTerm: "whale"},
		{keyword: "MAN", wantTerm: "man"},
		{keyword: "the whale", wantTerm: "whale"},
		{keyword: "Moby-Dick", wantTerm: "moby"},
		{keyword: "XIV Sherlock", wantTerm: "sherlock"},
	}
	for _, testCase := range cases {
		t.Run(testCase.keyword, func(t *testing.T) {
			searcher := &fakeSearcher{result: mongodb.SearchResult{Books: []models.BookMetadata{}}}

			_, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: testCase.keyword, Limit: api.DefaultLimit})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if searcher.received.Term != testCase.wantTerm {
				t.Errorf("datamart term = %q, want %q", searcher.received.Term, testCase.wantTerm)
			}
		})
	}
}

func TestDatamartEngineAnswersUnindexableKeywordWithoutQueryingDatamart(t *testing.T) {
	for _, keyword := range []string{"the", "a", "XIV", "1813", "¿?"} {
		t.Run(keyword, func(t *testing.T) {
			searcher := &fakeSearcher{err: errors.New("the datamart must not be queried")}

			page, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: keyword, Limit: api.DefaultLimit})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if searcher.calls != 0 {
				t.Errorf("datamart was queried %d times for a keyword that is never indexed", searcher.calls)
			}
			if page.Total != 0 || page.Books == nil || len(page.Books) != 0 {
				t.Errorf("page = %+v, want total 0 and a non-nil empty slice", page)
			}
		})
	}
}

func TestDatamartEngineReturnsDatamartPage(t *testing.T) {
	books := []models.BookMetadata{
		{BookID: 2, Title: "More Adventures", Author: "jane austin", Language: "en,fr", PublicationYear: 1850},
		{BookID: 1, Title: "Island Adventure", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
	}
	searcher := &fakeSearcher{result: mongodb.SearchResult{Total: 7, Books: books}}

	page, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: "adventure", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.Total != 7 || !slices.Equal(page.Books, books) {
		t.Errorf("page = %+v, want total 7 and the datamart books in datamart order", page)
	}
}

func TestDatamartEnginePropagatesDatamartFailure(t *testing.T) {
	failure := errors.New("server selection timeout")
	searcher := &fakeSearcher{err: failure}

	_, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: "whale", Limit: 1})
	if !errors.Is(err, failure) {
		t.Errorf("Search error = %v, want the datamart failure", err)
	}
}

// TestSearchRouteIsServedByDatamartEngine drives GET /search through the router run() builds,
// proving the production path reaches the datamart engine and its JSON contract holds.
func TestSearchRouteIsServedByDatamartEngine(t *testing.T) {
	searcher := &fakeSearcher{result: mongodb.SearchResult{Total: 1, Books: []models.BookMetadata{
		{BookID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
	}}}
	router := newTestRouter(t, searcher)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=Prejudice&language=EN", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	want := `{"query":"Prejudice","filters":{"language":"en"},"total":1,"offset":0,"limit":20,` +
		`"results":[{"book_id":1342,"title":"Pride and Prejudice","author":"Jane Austen","language":"en","year":1813}]}`
	if body := trimNewline(recorder.Body.String()); body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
	if searcher.received.Term != "prejudice" || searcher.received.Language != "en" {
		t.Errorf("datamart request = %+v, want term prejudice and language en", searcher.received)
	}
}

func TestSearchRouteReportsDatamartFailureAsInternalError(t *testing.T) {
	router := newTestRouter(t, &fakeSearcher{err: errors.New("connection refused")})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=whale", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
}

func newTestRouter(t *testing.T, searcher bookSearcher) http.Handler {
	t.Helper()
	searchHandler, err := api.NewSearchHandler(NewDatamartEngine(searcher))
	if err != nil {
		t.Fatalf("NewSearchHandler: %v", err)
	}
	catalogHandler, err := api.NewCatalogHandler(emptyCatalog{})
	if err != nil {
		t.Fatalf("NewCatalogHandler: %v", err)
	}
	return newRouter(searchHandler, catalogHandler)
}

func trimNewline(body string) string {
	if len(body) > 0 && body[len(body)-1] == '\n' {
		return body[:len(body)-1]
	}
	return body
}
