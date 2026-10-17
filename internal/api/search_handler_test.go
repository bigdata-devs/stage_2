package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/models"
)

// recordingEngine is a SearchEngine fake that remembers the last query it received and answers
// with a fixed page or error.
type recordingEngine struct {
	page          SearchPage
	err           error
	calls         int
	receivedQuery SearchQuery
}

func (engine *recordingEngine) Search(_ context.Context, query SearchQuery) (SearchPage, error) {
	engine.calls++
	engine.receivedQuery = query
	return engine.page, engine.err
}

func performSearch(t *testing.T, engine SearchEngine, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewSearchHandler(engine)
	if err != nil {
		t.Fatalf("NewSearchHandler: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.Search(recorder, httptest.NewRequest(http.MethodGet, "/search?"+rawQuery, nil))
	return recorder
}

func decodeBody[Body any](t *testing.T, recorder *httptest.ResponseRecorder) Body {
	t.Helper()
	var body Body
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, recorder.Body.String())
	}
	return body
}

// rawField returns one top-level field of the response exactly as it was serialized.
func rawField(t *testing.T, recorder *httptest.ResponseRecorder, name string) string {
	t.Helper()
	fields := decodeBody[map[string]json.RawMessage](t, recorder)
	field, found := fields[name]
	if !found {
		t.Fatalf("response has no %q field: %s", name, recorder.Body.String())
	}
	return string(field)
}

func assertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, want, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
}

func resultIDs(response SearchResponse) []models.BookID {
	ids := make([]models.BookID, 0, len(response.Results))
	for _, result := range response.Results {
		ids = append(ids, result.BookID)
	}
	return ids
}

func TestSearchReturnsMatchingBooksWithDefaultPagination(t *testing.T) {
	recorder := performSearch(t, NewStubEngine(), "q=adventure")

	assertStatus(t, recorder, http.StatusOK)
	response := decodeBody[SearchResponse](t, recorder)
	if response.Query != "adventure" || response.Total != 5 || response.Offset != 0 || response.Limit != DefaultLimit {
		t.Errorf("envelope = %+v, want query adventure, total 5, offset 0, limit %d", response, DefaultLimit)
	}
	if want := []models.BookID{11, 74, 76, 521, 1661}; !slices.Equal(resultIDs(response), want) {
		t.Errorf("result IDs = %v, want %v", resultIDs(response), want)
	}
	if filters := rawField(t, recorder, "filters"); filters != "{}" {
		t.Errorf("filters = %s, want {} when no filter is sent", filters)
	}
}

func TestSearchMapsBookMetadataToResultSchema(t *testing.T) {
	recorder := performSearch(t, NewStubEngine(), "q=pride")

	assertStatus(t, recorder, http.StatusOK)
	want := `[{"book_id":1342,"title":"Pride and Prejudice","author":"Jane Austen","language":"en","year":1813}]`
	if results := rawField(t, recorder, "results"); results != want {
		t.Errorf("results = %s, want %s", results, want)
	}
}

func TestSearchCombinesFilters(t *testing.T) {
	recorder := performSearch(t, NewStubEngine(), "q=adventure&author=twain&language=EN&year=1884")

	assertStatus(t, recorder, http.StatusOK)
	response := decodeBody[SearchResponse](t, recorder)
	if want := []models.BookID{76}; response.Total != 1 || !slices.Equal(resultIDs(response), want) {
		t.Errorf("total = %d, IDs = %v, want 1 match %v", response.Total, resultIDs(response), want)
	}
	if filters := rawField(t, recorder, "filters"); filters != `{"author":"twain","language":"en","year":1884}` {
		t.Errorf("filters = %s, want every applied filter with the language lower-cased", filters)
	}
}

func TestSearchEchoesOnlyTheFiltersThatWereSent(t *testing.T) {
	recorder := performSearch(t, NewStubEngine(), "q=the&language=fr&author=")

	assertStatus(t, recorder, http.StatusOK)
	if filters := rawField(t, recorder, "filters"); filters != `{"language":"fr"}` {
		t.Errorf("filters = %s, want only the language filter", filters)
	}
}

func TestSearchForwardsValidatedQueryToEngine(t *testing.T) {
	engine := &recordingEngine{}
	performSearch(t, engine, "q=%20whale%20&author=Melville&language=En&year=1851&offset=40&limit=10")

	want := SearchQuery{
		Term:    "whale",
		Filters: SearchFilters{Author: "Melville", Language: "en", Year: 1851},
		Offset:  40,
		Limit:   10,
	}
	if engine.calls != 1 || engine.receivedQuery != want {
		t.Errorf("engine got %d calls with %+v, want 1 call with %+v", engine.calls, engine.receivedQuery, want)
	}
}

func TestSearchPaginatesMatches(t *testing.T) {
	cases := []struct {
		name     string
		rawQuery string
		wantIDs  []models.BookID
	}{
		{name: "first page", rawQuery: "q=adventure&limit=2", wantIDs: []models.BookID{11, 74}},
		{name: "partial last page", rawQuery: "q=adventure&offset=4&limit=2", wantIDs: []models.BookID{1661}},
		{name: "offset past the last match", rawQuery: "q=adventure&offset=50", wantIDs: []models.BookID{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := performSearch(t, NewStubEngine(), testCase.rawQuery)

			assertStatus(t, recorder, http.StatusOK)
			response := decodeBody[SearchResponse](t, recorder)
			if response.Total != 5 || !slices.Equal(resultIDs(response), testCase.wantIDs) {
				t.Errorf("total = %d, IDs = %v, want total 5 and %v", response.Total, resultIDs(response), testCase.wantIDs)
			}
		})
	}
}

func TestSearchWithoutMatchesReturnsEmptyResults(t *testing.T) {
	recorder := performSearch(t, NewStubEngine(), "q=nonexistentword")

	assertStatus(t, recorder, http.StatusOK)
	if total := rawField(t, recorder, "total"); total != "0" {
		t.Errorf("total = %s, want 0", total)
	}
	if results := rawField(t, recorder, "results"); results != "[]" {
		t.Errorf("results = %s, want [] (never null)", results)
	}
}

func TestSearchSerializesEmptyResultsWhenEngineReturnsNoSlice(t *testing.T) {
	recorder := performSearch(t, &recordingEngine{page: SearchPage{}}, "q=anything")

	assertStatus(t, recorder, http.StatusOK)
	if results := rawField(t, recorder, "results"); results != "[]" {
		t.Errorf("results = %s, want [] (never null)", results)
	}
}

func TestSearchWithoutQueryReturnsBadRequest(t *testing.T) {
	for _, rawQuery := range []string{"", "q=", "q=%20%20", "author=Austen&language=en"} {
		t.Run("query string "+rawQuery, func(t *testing.T) {
			engine := &recordingEngine{}
			recorder := performSearch(t, engine, rawQuery)

			assertStatus(t, recorder, http.StatusBadRequest)
			response := decodeBody[ErrorResponse](t, recorder)
			if response.Error != missingParameterCode || !strings.Contains(response.Message, `"q"`) {
				t.Errorf("error = %+v, want %s naming q", response, missingParameterCode)
			}
			if engine.calls != 0 {
				t.Errorf("engine was called %d times for an invalid request", engine.calls)
			}
		})
	}
}

func TestSearchWithMalformedParameterReturnsBadRequest(t *testing.T) {
	malformedQueries := []string{
		"q=whale&year=eighteen",
		"q=whale&year=0",
		"q=whale&offset=-1",
		"q=whale&offset=first",
		"q=whale&limit=0",
		"q=whale&limit=101",
		"q=whale&limit=2.5",
	}
	for _, rawQuery := range malformedQueries {
		t.Run(rawQuery, func(t *testing.T) {
			recorder := performSearch(t, &recordingEngine{}, rawQuery)

			assertStatus(t, recorder, http.StatusBadRequest)
			if response := decodeBody[ErrorResponse](t, recorder); response.Error != invalidParameterCode {
				t.Errorf("error = %+v, want %s", response, invalidParameterCode)
			}
		})
	}
}

func TestSearchEngineFailureReturnsInternalError(t *testing.T) {
	engine := &recordingEngine{err: errors.New("mongo: connection refused")}
	recorder := performSearch(t, engine, "q=whale")

	assertStatus(t, recorder, http.StatusInternalServerError)
	response := decodeBody[ErrorResponse](t, recorder)
	if response.Error != internalErrorCode || strings.Contains(response.Message, "mongo") {
		t.Errorf("error = %+v, want %s without internal details", response, internalErrorCode)
	}
}

func TestNewSearchHandlerRejectsMissingEngine(t *testing.T) {
	if _, err := NewSearchHandler(nil); !errors.Is(err, ErrMissingEngine) {
		t.Errorf("NewSearchHandler(nil) error = %v, want ErrMissingEngine", err)
	}
}
