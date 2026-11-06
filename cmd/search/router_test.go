package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type fakeIndex struct {
	page     search.Page
	err      error
	calls    int
	received search.Query
}

func (index *fakeIndex) Search(_ context.Context, query search.Query) (search.Page, error) {
	index.calls++
	index.received = query
	return index.page, index.err
}

type emptyCatalog struct{}

func (emptyCatalog) PublishedBook(context.Context, models.BookID) (models.BookMetadata, bool, error) {
	return models.BookMetadata{}, false, nil
}

func (emptyCatalog) DistinctFilterValues(context.Context) (search.StoredFilterValues, error) {
	return search.StoredFilterValues{}, nil
}

func TestSearchRouteIsServedThroughTheSearchCore(t *testing.T) {
	index := &fakeIndex{page: search.Page{Total: 1, Books: []models.BookMetadata{
		{BookID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
	}}}
	router := newTestRouter(t, index)

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
	if !slices.Equal(index.received.Terms, []string{"prejudice"}) || index.received.Filters.Language != "en" {
		t.Errorf("index query = %+v, want terms [prejudice] and language en", index.received)
	}
}

func TestSearchRouteSendsEveryWordToTheIndex(t *testing.T) {
	index := &fakeIndex{page: search.Page{Books: []models.BookMetadata{}}}
	router := newTestRouter(t, index)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=Moby-Dick%2C+the+WHALE+whale", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if want := []string{"moby", "dick", "whale"}; index.calls != 1 || !slices.Equal(index.received.Terms, want) {
		t.Errorf("index got %d calls with terms %q, want 1 call with %q", index.calls, index.received.Terms, want)
	}
}

func TestSearchRouteAnswersUnsearchableQueriesWithoutTheIndex(t *testing.T) {
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
			index := &fakeIndex{err: errors.New("the index must not be queried")}
			router := newTestRouter(t, index)

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?"+testCase.rawQuery, nil))

			if recorder.Code != testCase.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if index.calls != 0 {
				t.Errorf("index was queried %d times", index.calls)
			}
		})
	}
}

func TestSearchRouteReportsAnIndexFailureAsInternalError(t *testing.T) {
	router := newTestRouter(t, &fakeIndex{err: errors.New("connection refused")})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/search?q=whale", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
}

func newTestRouter(t *testing.T, index search.Index) http.Handler {
	t.Helper()
	searchHandler, err := buildSearchHandler(index)
	if err != nil {
		t.Fatalf("buildSearchHandler: %v", err)
	}
	catalogHandler, err := buildCatalogHandler(emptyCatalog{})
	if err != nil {
		t.Fatalf("buildCatalogHandler: %v", err)
	}
	return newRouter(searchHandler, catalogHandler)
}

func trimNewline(body string) string {
	if len(body) > 0 && body[len(body)-1] == '\n' {
		return body[:len(body)-1]
	}
	return body
}
