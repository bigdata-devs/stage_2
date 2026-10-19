package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boogle-engine/internal/models"
)

// fakeCatalog is a BookCatalog fake that answers with fixed values or errors.
type fakeCatalog struct {
	book      models.BookMetadata
	found     bool
	bookErr   error
	values    models.FilterValues
	valuesErr error
}

func (catalog *fakeCatalog) PublishedBook(_ context.Context, bookID models.BookID) (models.BookMetadata, bool, error) {
	if catalog.bookErr != nil {
		return models.BookMetadata{}, false, catalog.bookErr
	}
	catalog.book.BookID = bookID
	return catalog.book, catalog.found, nil
}

func (catalog *fakeCatalog) FilterOptions(context.Context) (models.FilterValues, error) {
	if catalog.valuesErr != nil {
		return models.FilterValues{}, catalog.valuesErr
	}
	return catalog.values, nil
}

// sendCatalog routes one request through a ServeMux so path parameters reach the handlers.
func sendCatalog(t *testing.T, catalog BookCatalog, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewCatalogHandler(catalog)
	if err != nil {
		t.Fatalf("NewCatalogHandler: %v", err)
	}
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func publishedStubCatalog() *fakeCatalog {
	return &fakeCatalog{
		found: true,
		book: models.BookMetadata{
			Title:           "Pride and Prejudice",
			Author:          "Jane Austen",
			Language:        "en",
			PublicationYear: 1813,
		},
	}
}

func TestBookReturnsPublishedBookMetadata(t *testing.T) {
	recorder := sendCatalog(t, publishedStubCatalog(), http.MethodGet, "/books/1342")

	assertStatus(t, recorder, http.StatusOK)
	want := `{"book_id":1342,"title":"Pride and Prejudice","author":"Jane Austen","language":"en","year":1813}`
	if body := strings.TrimSpace(recorder.Body.String()); body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestBookOfMissingOrUnpublishedBookReturnsNotFound(t *testing.T) {
	cases := []struct {
		name    string
		catalog *fakeCatalog
		target  string
	}{
		{name: "unknown book", catalog: &fakeCatalog{found: false}, target: "/books/9999"},
		{name: "book awaiting publication", catalog: &fakeCatalog{found: false}, target: "/books/42"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := sendCatalog(t, testCase.catalog, http.MethodGet, testCase.target)

			assertStatus(t, recorder, http.StatusNotFound)
			if response := decodeBody[ErrorResponse](t, recorder); response.Error != bookNotFoundCode {
				t.Errorf("error = %+v, want %s", response, bookNotFoundCode)
			}
		})
	}
}

func TestBookWithMalformedIDReturnsBadRequest(t *testing.T) {
	for _, target := range []string{"/books/abc", "/books/0", "/books/-7", "/books/1.5"} {
		t.Run(target, func(t *testing.T) {
			catalog := &fakeCatalog{}
			recorder := sendCatalog(t, catalog, http.MethodGet, target)

			assertStatus(t, recorder, http.StatusBadRequest)
			if response := decodeBody[ErrorResponse](t, recorder); response.Error != invalidBookIDCode {
				t.Errorf("error = %+v, want %s", response, invalidBookIDCode)
			}
			if catalog.book.BookID != 0 {
				t.Errorf("catalog was consulted with book ID %d for an invalid request", catalog.book.BookID)
			}
		})
	}
}

func TestBookFailureReturnsInternalErrorWithoutDetails(t *testing.T) {
	catalog := &fakeCatalog{bookErr: errors.New("mongo: connection refused")}
	recorder := sendCatalog(t, catalog, http.MethodGet, "/books/1342")

	assertStatus(t, recorder, http.StatusInternalServerError)
	response := decodeBody[ErrorResponse](t, recorder)
	if response.Error != internalErrorCode || len(response.Message) == 0 {
		t.Errorf("error = %+v, want %s with a generic message", response, internalErrorCode)
	}
	if encoded, _ := json.Marshal(response); strings.Contains(string(encoded), "mongo") {
		t.Errorf("response leaks internal details: %s", encoded)
	}
}

func TestFiltersReturnsDistinctPublishedValues(t *testing.T) {
	catalog := &fakeCatalog{values: models.FilterValues{
		Languages: []string{"en", "es", "fr"},
		Years:     []int{1719, 1813, 1851},
	}}
	recorder := sendCatalog(t, catalog, http.MethodGet, "/search/filters")

	assertStatus(t, recorder, http.StatusOK)
	want := `{"languages":["en","es","fr"],"years":[1719,1813,1851]}`
	if body := strings.TrimSpace(recorder.Body.String()); body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestFiltersSerializesEmptyArraysWhenNoBooksArePublished(t *testing.T) {
	recorder := sendCatalog(t, &fakeCatalog{}, http.MethodGet, "/search/filters")

	assertStatus(t, recorder, http.StatusOK)
	if languages := rawField(t, recorder, "languages"); languages != "[]" {
		t.Errorf("languages = %s, want [] (never null)", languages)
	}
	if years := rawField(t, recorder, "years"); years != "[]" {
		t.Errorf("years = %s, want [] (never null)", years)
	}
}

func TestFiltersFailureReturnsInternalError(t *testing.T) {
	catalog := &fakeCatalog{valuesErr: errors.New("mongo: socket closed")}
	recorder := sendCatalog(t, catalog, http.MethodGet, "/search/filters")

	assertStatus(t, recorder, http.StatusInternalServerError)
	if response := decodeBody[ErrorResponse](t, recorder); response.Error != internalErrorCode {
		t.Errorf("error = %+v, want %s", response, internalErrorCode)
	}
}

func TestCatalogRoutesRejectOtherMethods(t *testing.T) {
	catalog := publishedStubCatalog()
	for _, testCase := range []struct{ method, target string }{
		{http.MethodPost, "/books/1342"},
		{http.MethodDelete, "/search/filters"},
	} {
		t.Run(testCase.method+" "+testCase.target, func(t *testing.T) {
			recorder := sendCatalog(t, catalog, testCase.method, testCase.target)
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", recorder.Code)
			}
		})
	}
}

func TestNewCatalogHandlerRejectsMissingCatalog(t *testing.T) {
	if _, err := NewCatalogHandler(nil); !errors.Is(err, ErrMissingCatalog) {
		t.Errorf("NewCatalogHandler(nil) error = %v, want ErrMissingCatalog", err)
	}
}
