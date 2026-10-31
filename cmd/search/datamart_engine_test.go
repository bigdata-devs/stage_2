package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		Term:    "Sherlock Holmes!",
		Terms:   []string{"sherlock", "holmes"},
		Filters: api.SearchFilters{Author: "Doyle", Language: "en", Year: 1892},
		Offset:  20,
		Limit:   10,
	}

	if _, err := NewDatamartEngine(searcher).Search(context.Background(), query); err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := mongodb.SearchRequest{
		Terms: []string{"sherlock", "holmes"}, Author: "Doyle", Language: "en", Year: 1892, Offset: 20, Limit: 10,
	}
	if searcher.calls != 1 || !reflect.DeepEqual(searcher.received, want) {
		t.Errorf("datamart got %d calls with %+v, want 1 call with %+v", searcher.calls, searcher.received, want)
	}
}

func TestDatamartEngineWithoutTermsDoesNotQueryDatamart(t *testing.T) {
	searcher := &fakeSearcher{err: errors.New("the datamart must not be queried")}

	page, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: "the", Limit: api.DefaultLimit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if searcher.calls != 0 {
		t.Errorf("datamart was queried %d times without terms", searcher.calls)
	}
	if page.Total != 0 || page.Books == nil || len(page.Books) != 0 {
		t.Errorf("page = %+v, want total 0 and a non-nil empty slice", page)
	}
}

func TestDatamartEngineReturnsDatamartPage(t *testing.T) {
	books := []models.BookMetadata{
		{BookID: 2, Title: "More Adventures", Author: "jane austin", Language: "en,fr", PublicationYear: 1850},
		{BookID: 1, Title: "Island Adventure", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
	}
	searcher := &fakeSearcher{result: mongodb.SearchResult{Total: 7, Books: books}}

	page, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: "adventure", Terms: []string{"adventure"}, Limit: 2})
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

	_, err := NewDatamartEngine(searcher).Search(context.Background(), api.SearchQuery{Term: "whale", Terms: []string{"whale"}, Limit: 1})
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
	if !slices.Equal(searcher.received.Terms, []string{"prejudice"}) || searcher.received.Language != "en" {
		t.Errorf("datamart request = %+v, want terms [prejudice] and language en", searcher.received)
	}
}

// TestSearchRouteSendsEveryWordToDatamart proves a multi-word q reaches the datamart as the AND
// of its normalized, distinct terms, with ignored words dropped.
func TestSearchRouteSendsEveryWordToDatamart(t *testing.T) {
	searcher := &fakeSearcher{result: mongodb.SearchResult{Books: []models.BookMetadata{}}}
	router := newTestRouter(t, searcher)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=Moby-Dick%2C+the+WHALE+whale", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if want := []string{"moby", "dick", "whale"}; searcher.calls != 1 || !slices.Equal(searcher.received.Terms, want) {
		t.Errorf("datamart got %d calls with terms %q, want 1 call with %q", searcher.calls, searcher.received.Terms, want)
	}
}

func TestSearchRouteAnswersUnsearchableQueriesWithoutDatamart(t *testing.T) {
	cases := []struct {
		rawQuery   string
		wantStatus int
	}{
		{rawQuery: "q=the+of+and", wantStatus: http.StatusOK},
		{rawQuery: "q=1813", wantStatus: http.StatusBadRequest},
		{rawQuery: "q=alpha+bravo+charlie+delta+echo+foxtrot+golf+hotel+india+juliet+kilo", wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.rawQuery, func(t *testing.T) {
			searcher := &fakeSearcher{err: errors.New("the datamart must not be queried")}
			router := newTestRouter(t, searcher)

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?"+testCase.rawQuery, nil))

			if recorder.Code != testCase.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if searcher.calls != 0 {
				t.Errorf("datamart was queried %d times", searcher.calls)
			}
		})
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
