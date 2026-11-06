package search_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

type recordingIndex struct {
	page     search.Page
	err      error
	calls    int
	received search.Query
}

func (index *recordingIndex) Search(_ context.Context, query search.Query) (search.Page, error) {
	index.calls++
	index.received = query
	return index.page, index.err
}

func newService(t *testing.T, index search.Index) *search.Service {
	t.Helper()
	service, err := search.NewService(index)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func number(value int) *int {
	return &value
}

func TestNewServiceRejectsMissingIndex(t *testing.T) {
	if _, err := search.NewService(nil); !errors.Is(err, search.ErrMissingIndex) {
		t.Errorf("NewService(nil) = %v, want ErrMissingIndex", err)
	}
}

func TestSearchNormalizesTheRequestIntoAQuery(t *testing.T) {
	index := &recordingIndex{}
	request := search.Request{
		Text:     "  Moby-Dick, the WHALE!  ",
		Author:   " Melville ",
		Language: " EN ",
		Year:     number(1851),
		Offset:   number(40),
		Limit:    number(10),
	}

	result, err := newService(t, index).Search(context.Background(), request)

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := search.Query{
		Text:    "Moby-Dick, the WHALE!",
		Terms:   []string{"moby", "dick", "whale"},
		Filters: search.Filters{Author: "Melville", Language: "en", Year: 1851},
		Offset:  40,
		Limit:   10,
	}
	if index.calls != 1 || !reflect.DeepEqual(index.received, want) {
		t.Errorf("index got %d calls with %+v, want 1 call with %+v", index.calls, index.received, want)
	}
	if !reflect.DeepEqual(result.Query, want) {
		t.Errorf("result query = %+v, want %+v", result.Query, want)
	}
}

func TestSearchAppliesTheDefaultPageWithoutFilters(t *testing.T) {
	index := &recordingIndex{}

	if _, err := newService(t, index).Search(context.Background(), search.Request{Text: "whale"}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := search.Query{Text: "whale", Terms: []string{"whale"}, Offset: 0, Limit: search.DefaultLimit}
	if !reflect.DeepEqual(index.received, want) {
		t.Errorf("index got %+v, want %+v", index.received, want)
	}
}

func TestSearchSplitsTheTextIntoDistinctTerms(t *testing.T) {
	cases := []struct {
		text      string
		wantTerms []string
	}{
		{text: "sherlock holmes", wantTerms: []string{"sherlock", "holmes"}},
		{text: "whale Whale WHALE ship", wantTerms: []string{"whale", "ship"}},
		{text: "Volume XIV of a Treasure", wantTerms: []string{"volume", "treasure"}},
		{text: "island1883treasure", wantTerms: []string{"island", "treasure"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.text, func(t *testing.T) {
			index := &recordingIndex{}

			if _, err := newService(t, index).Search(context.Background(), search.Request{Text: testCase.text}); err != nil {
				t.Fatalf("Search: %v", err)
			}

			if !slices.Equal(index.received.Terms, testCase.wantTerms) {
				t.Errorf("terms = %q, want %q", index.received.Terms, testCase.wantTerms)
			}
		})
	}
}

func TestSearchAcceptsExactlyMaxQueryTermsDistinctWords(t *testing.T) {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	text := strings.Join(words[:search.MaxQueryTerms], " ") + " alpha bravo the of and"
	index := &recordingIndex{}

	if _, err := newService(t, index).Search(context.Background(), search.Request{Text: text}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(index.received.Terms) != search.MaxQueryTerms {
		t.Errorf("terms = %q, want %d distinct terms", index.received.Terms, search.MaxQueryTerms)
	}
}

func TestSearchRejectsInvalidRequestsWithoutQueryingTheIndex(t *testing.T) {
	overLimit := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo"
	cases := []struct {
		name        string
		request     search.Request
		wantField   search.Field
		wantMissing bool
		wantRule    string
	}{
		{name: "missing text", request: search.Request{Text: "   "}, wantField: search.FieldText, wantMissing: true},
		{name: "text without words", request: search.Request{Text: "1813 ?!"}, wantField: search.FieldText, wantRule: "at least one word"},
		{name: "too many terms", request: search.Request{Text: overLimit}, wantField: search.FieldText, wantRule: fmt.Sprint(search.MaxQueryTerms)},
		{name: "year zero", request: search.Request{Text: "whale", Year: number(0)}, wantField: search.FieldYear, wantRule: "year between"},
		{name: "year too late", request: search.Request{Text: "whale", Year: number(10000)}, wantField: search.FieldYear, wantRule: "year between"},
		{name: "negative offset", request: search.Request{Text: "whale", Offset: number(-1)}, wantField: search.FieldOffset, wantRule: "non-negative"},
		{name: "limit zero", request: search.Request{Text: "whale", Limit: number(0)}, wantField: search.FieldLimit, wantRule: "between 1 and 100"},
		{name: "limit too large", request: search.Request{Text: "whale", Limit: number(search.MaxLimit + 1)}, wantField: search.FieldLimit, wantRule: "between 1 and 100"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			index := &recordingIndex{}

			_, err := newService(t, index).Search(context.Background(), testCase.request)

			var invalid search.InvalidQueryError
			if !errors.Is(err, search.ErrInvalidQuery) || !errors.As(err, &invalid) {
				t.Fatalf("Search = %v, want an InvalidQueryError", err)
			}
			if invalid.Field != testCase.wantField || invalid.Missing != testCase.wantMissing || !strings.Contains(invalid.Rule, testCase.wantRule) {
				t.Errorf("error = %+v, want field %s, missing %t and a rule containing %q", invalid, testCase.wantField, testCase.wantMissing, testCase.wantRule)
			}
			if index.calls != 0 {
				t.Errorf("index was called %d times for an invalid request", index.calls)
			}
		})
	}
}

func TestInvalidNumberStatesTheSameRuleAsTheRangeCheck(t *testing.T) {
	cases := []struct {
		field   search.Field
		request search.Request
	}{
		{field: search.FieldYear, request: search.Request{Text: "whale", Year: number(0)}},
		{field: search.FieldOffset, request: search.Request{Text: "whale", Offset: number(-1)}},
		{field: search.FieldLimit, request: search.Request{Text: "whale", Limit: number(0)}},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.field), func(t *testing.T) {
			_, err := newService(t, &recordingIndex{}).Search(context.Background(), testCase.request)

			if want := search.InvalidNumber(testCase.field); !errors.Is(err, search.ErrInvalidQuery) || err.Error() != want.Error() {
				t.Errorf("range error = %v, want %v", err, want)
			}
		})
	}
}

func TestSearchOfOnlyIgnoredWordsReturnsAnEmptyPageWithoutTheIndex(t *testing.T) {
	index := &recordingIndex{err: errors.New("the index must not be called")}

	result, err := newService(t, index).Search(context.Background(), search.Request{Text: "The Of And", Limit: number(5)})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if index.calls != 0 {
		t.Errorf("index was called %d times for a query no book can match", index.calls)
	}
	if result.Query.Text != "The Of And" || len(result.Query.Terms) != 0 || result.Query.Limit != 5 {
		t.Errorf("result query = %+v, want the text echoed, no terms and limit 5", result.Query)
	}
	if result.Page.Total != 0 || result.Page.Books == nil || len(result.Page.Books) != 0 {
		t.Errorf("page = %+v, want total 0 and a non-nil empty slice", result.Page)
	}
}

func TestSearchReturnsTheIndexPage(t *testing.T) {
	books := []models.BookMetadata{
		{BookID: 2, Title: "More Adventures"},
		{BookID: 1, Title: "Island Adventure"},
	}
	index := &recordingIndex{page: search.Page{Total: 7, Books: books}}

	result, err := newService(t, index).Search(context.Background(), search.Request{Text: "adventure"})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Page.Total != 7 || !slices.Equal(result.Page.Books, books) {
		t.Errorf("page = %+v, want total 7 and the index books in index order", result.Page)
	}
}

func TestSearchNeverReturnsANilBookSlice(t *testing.T) {
	result, err := newService(t, &recordingIndex{}).Search(context.Background(), search.Request{Text: "whale"})

	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Page.Books == nil {
		t.Error("books is nil, want an empty slice")
	}
}

func TestSearchReportsAnIndexFailureAsAnInfrastructureError(t *testing.T) {
	failure := errors.New("server selection timeout")

	_, err := newService(t, &recordingIndex{err: failure}).Search(context.Background(), search.Request{Text: "whale"})

	if !errors.Is(err, failure) || errors.Is(err, search.ErrInvalidQuery) {
		t.Errorf("Search = %v, want the index failure and no validation error", err)
	}
}
