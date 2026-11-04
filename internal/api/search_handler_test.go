package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type recordingIndex struct {
	page          search.Page
	err           error
	calls         int
	receivedQuery search.Query
}

func (index *recordingIndex) Search(_ context.Context, query search.Query) (search.Page, error) {
	index.calls++
	index.receivedQuery = query
	return index.page, index.err
}

func performSearch(t *testing.T, index search.Index, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	service, err := search.NewService(index)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	handler, err := NewSearchHandler(service)
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
	recorder := performSearch(t, NewStubIndex(), "q=adventure")

	assertStatus(t, recorder, http.StatusOK)
	response := decodeBody[SearchResponse](t, recorder)
	if response.Query != "adventure" || response.Total != 5 || response.Offset != 0 || response.Limit != search.DefaultLimit {
		t.Errorf("envelope = %+v, want query adventure, total 5, offset 0, limit %d", response, search.DefaultLimit)
	}
	if want := []models.BookID{11, 74, 76, 521, 1661}; !slices.Equal(resultIDs(response), want) {
		t.Errorf("result IDs = %v, want %v", resultIDs(response), want)
	}
	if filters := rawField(t, recorder, "filters"); filters != "{}" {
		t.Errorf("filters = %s, want {} when no filter is sent", filters)
	}
}

func TestSearchMapsBookMetadataToResultSchema(t *testing.T) {
	recorder := performSearch(t, NewStubIndex(), "q=pride")

	assertStatus(t, recorder, http.StatusOK)
	want := `[{"book_id":1342,"title":"Pride and Prejudice","author":"Jane Austen","language":"en","year":1813}]`
	if results := rawField(t, recorder, "results"); results != want {
		t.Errorf("results = %s, want %s", results, want)
	}
}

func TestSearchCombinesFilters(t *testing.T) {
	recorder := performSearch(t, NewStubIndex(), "q=adventure&author=twain&language=EN&year=1884")

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
	recorder := performSearch(t, NewStubIndex(), "q=the&language=fr&author=")

	assertStatus(t, recorder, http.StatusOK)
	if filters := rawField(t, recorder, "filters"); filters != `{"language":"fr"}` {
		t.Errorf("filters = %s, want only the language filter", filters)
	}
}

func TestSearchForwardsValidatedQueryToIndex(t *testing.T) {
	index := &recordingIndex{}
	performSearch(t, index, "q=%20whale%20&author=Melville&language=En&year=1851&offset=40&limit=10")

	want := search.Query{
		Text:    "whale",
		Terms:   []string{"whale"},
		Filters: search.Filters{Author: "Melville", Language: "en", Year: 1851},
		Offset:  40,
		Limit:   10,
	}
	if index.calls != 1 || !reflect.DeepEqual(index.receivedQuery, want) {
		t.Errorf("index got %d calls with %+v, want 1 call with %+v", index.calls, index.receivedQuery, want)
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
			recorder := performSearch(t, NewStubIndex(), testCase.rawQuery)

			assertStatus(t, recorder, http.StatusOK)
			response := decodeBody[SearchResponse](t, recorder)
			if response.Total != 5 || !slices.Equal(resultIDs(response), testCase.wantIDs) {
				t.Errorf("total = %d, IDs = %v, want total 5 and %v", response.Total, resultIDs(response), testCase.wantIDs)
			}
		})
	}
}

func TestSearchWithoutMatchesReturnsEmptyResults(t *testing.T) {
	recorder := performSearch(t, NewStubIndex(), "q=nonexistentword")

	assertStatus(t, recorder, http.StatusOK)
	if total := rawField(t, recorder, "total"); total != "0" {
		t.Errorf("total = %s, want 0", total)
	}
	if results := rawField(t, recorder, "results"); results != "[]" {
		t.Errorf("results = %s, want [] (never null)", results)
	}
}

func TestSearchSerializesEmptyResultsWhenIndexReturnsNoSlice(t *testing.T) {
	recorder := performSearch(t, &recordingIndex{page: search.Page{}}, "q=anything")

	assertStatus(t, recorder, http.StatusOK)
	if results := rawField(t, recorder, "results"); results != "[]" {
		t.Errorf("results = %s, want [] (never null)", results)
	}
}

func TestSearchWithoutQueryReturnsBadRequest(t *testing.T) {
	for _, rawQuery := range []string{"", "q=", "q=%20%20", "author=Austen&language=en"} {
		t.Run("query string "+rawQuery, func(t *testing.T) {
			index := &recordingIndex{}
			recorder := performSearch(t, index, rawQuery)

			assertStatus(t, recorder, http.StatusBadRequest)
			response := decodeBody[ErrorResponse](t, recorder)
			if response.Error != missingParameterCode || !strings.Contains(response.Message, `"q"`) {
				t.Errorf("error = %+v, want %s naming q", response, missingParameterCode)
			}
			if index.calls != 0 {
				t.Errorf("index was called %d times for an invalid request", index.calls)
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
			recorder := performSearch(t, &recordingIndex{}, rawQuery)

			assertStatus(t, recorder, http.StatusBadRequest)
			if response := decodeBody[ErrorResponse](t, recorder); response.Error != invalidParameterCode {
				t.Errorf("error = %+v, want %s", response, invalidParameterCode)
			}
		})
	}
}

func TestSearchIndexFailureReturnsInternalError(t *testing.T) {
	index := &recordingIndex{err: errors.New("mongo: connection refused")}
	recorder := performSearch(t, index, "q=whale")

	assertStatus(t, recorder, http.StatusInternalServerError)
	response := decodeBody[ErrorResponse](t, recorder)
	if response.Error != internalErrorCode || strings.Contains(response.Message, "mongo") {
		t.Errorf("error = %+v, want %s without internal details", response, internalErrorCode)
	}
}

func TestNewSearchHandlerRejectsMissingSearcher(t *testing.T) {
	if _, err := NewSearchHandler(nil); !errors.Is(err, ErrMissingSearcher) {
		t.Errorf("NewSearchHandler(nil) error = %v, want ErrMissingSearcher", err)
	}
}

func TestSearchSplitsMultiWordQueryIntoDistinctTerms(t *testing.T) {
	cases := []struct {
		rawQ      string
		wantTerm  string
		wantTerms []string
	}{
		{rawQ: "sherlock holmes", wantTerm: "sherlock holmes", wantTerms: []string{"sherlock", "holmes"}},
		{rawQ: "  Moby-Dick, the WHALE!  ", wantTerm: "Moby-Dick, the WHALE!", wantTerms: []string{"moby", "dick", "whale"}},
		{rawQ: "whale Whale WHALE ship", wantTerm: "whale Whale WHALE ship", wantTerms: []string{"whale", "ship"}},
		{rawQ: "Volume XIV of a Treasure", wantTerm: "Volume XIV of a Treasure", wantTerms: []string{"volume", "treasure"}},
		{rawQ: "island1883treasure", wantTerm: "island1883treasure", wantTerms: []string{"island", "treasure"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.rawQ, func(t *testing.T) {
			index := &recordingIndex{}
			recorder := performSearch(t, index, "q="+url.QueryEscape(testCase.rawQ))

			assertStatus(t, recorder, http.StatusOK)
			if index.receivedQuery.Text != testCase.wantTerm || !slices.Equal(index.receivedQuery.Terms, testCase.wantTerms) {
				t.Errorf("index got term %q terms %q, want %q and %q",
					index.receivedQuery.Text, index.receivedQuery.Terms, testCase.wantTerm, testCase.wantTerms)
			}
			if response := decodeBody[SearchResponse](t, recorder); response.Query != testCase.wantTerm {
				t.Errorf("query echo = %q, want q as sent without surrounding spaces %q", response.Query, testCase.wantTerm)
			}
		})
	}
}

func TestSearchRequiresEveryWordToMatch(t *testing.T) {
	cases := []struct {
		rawQ    string
		wantIDs []models.BookID
	}{
		{rawQ: "adventures sherlock", wantIDs: []models.BookID{1661}},
		{rawQ: "sherlock adventures", wantIDs: []models.BookID{1661}},
		{rawQ: "adventures of huckleberry", wantIDs: []models.BookID{76}},
		{rawQ: "adventure whale", wantIDs: []models.BookID{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.rawQ, func(t *testing.T) {
			recorder := performSearch(t, NewStubIndex(), "q="+url.QueryEscape(testCase.rawQ))

			assertStatus(t, recorder, http.StatusOK)
			response := decodeBody[SearchResponse](t, recorder)
			if response.Total != len(testCase.wantIDs) || !slices.Equal(resultIDs(response), testCase.wantIDs) {
				t.Errorf("total = %d, IDs = %v, want %v", response.Total, resultIDs(response), testCase.wantIDs)
			}
		})
	}
}

func TestSearchWithOnlyIgnoredWordsReturnsEmptyPageWithoutIndex(t *testing.T) {
	for _, rawQ := range []string{"the", "The Of And", "XIV", "a b c", "to be or not to be"} {
		t.Run(rawQ, func(t *testing.T) {
			index := &recordingIndex{err: errors.New("the index must not be called")}
			recorder := performSearch(t, index, "q="+url.QueryEscape(rawQ)+"&language=fr&limit=5")

			assertStatus(t, recorder, http.StatusOK)
			want := fmt.Sprintf(`{"query":%q,"filters":{"language":"fr"},"total":0,"offset":0,"limit":5,"results":[]}`, rawQ)
			if body := strings.TrimSpace(recorder.Body.String()); body != want {
				t.Errorf("body = %s, want %s", body, want)
			}
			if index.calls != 0 {
				t.Errorf("index was called %d times for a query no book can match", index.calls)
			}
		})
	}
}

func TestSearchWithoutAnyWordReturnsBadRequest(t *testing.T) {
	for _, rawQ := range []string{"1813", "?!", "42 - 17", "¿¡", "日本語", "ÉÉÉ"} {
		t.Run(rawQ, func(t *testing.T) {
			index := &recordingIndex{}
			recorder := performSearch(t, index, "q="+url.QueryEscape(rawQ))

			assertStatus(t, recorder, http.StatusBadRequest)
			response := decodeBody[ErrorResponse](t, recorder)
			if response.Error != invalidParameterCode || !strings.Contains(response.Message, `"q"`) {
				t.Errorf("error = %+v, want %s naming q", response, invalidParameterCode)
			}
			if index.calls != 0 {
				t.Errorf("index was called %d times for an invalid request", index.calls)
			}
		})
	}
}

func TestSearchLimitsTheNumberOfDistinctTerms(t *testing.T) {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo"}
	atLimit := strings.Join(words[:search.MaxQueryTerms], " ")
	cases := []struct {
		name       string
		rawQ       string
		wantStatus int
	}{
		{name: "exactly the limit", rawQ: atLimit, wantStatus: http.StatusOK},
		{name: "limit plus repeats and stop words", rawQ: atLimit + " alpha bravo the of and", wantStatus: http.StatusOK},
		{name: "one distinct term over the limit", rawQ: atLimit + " kilo", wantStatus: http.StatusBadRequest},
		{name: "a very long query", rawQ: strings.Repeat(atLimit+" kilo ", 10000), wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			index := &recordingIndex{}
			recorder := performSearch(t, index, "q="+url.QueryEscape(testCase.rawQ))

			assertStatus(t, recorder, testCase.wantStatus)
			if testCase.wantStatus == http.StatusBadRequest {
				response := decodeBody[ErrorResponse](t, recorder)
				if response.Error != invalidParameterCode || !strings.Contains(response.Message, fmt.Sprint(search.MaxQueryTerms)) {
					t.Errorf("error = %+v, want %s stating the limit %d", response, invalidParameterCode, search.MaxQueryTerms)
				}
				if index.calls != 0 {
					t.Errorf("index was called %d times for an invalid request", index.calls)
				}
			}
		})
	}
}
