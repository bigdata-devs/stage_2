// Package api exposes the front-end-facing HTTP API of the Boogle Engine Search Service.
// Handlers depend only on the SearchEngine interface, so the storage behind the search
// (the StubEngine sample catalog today, the MongoDB datamart later) can change without
// touching any HTTP code.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"boogle-engine/internal/models"
)

const (
	// DefaultLimit is the page size used when a request does not send "limit".
	DefaultLimit = 20
	// MaxLimit is the largest page size a client may request.
	MaxLimit = 100
)

// Error codes returned in ErrorResponse.Error by the search endpoint.
const (
	missingParameterCode = "missing_parameter"
	invalidParameterCode = "invalid_parameter"
)

// ErrMissingEngine is returned by NewSearchHandler when it receives no SearchEngine.
var ErrMissingEngine = errors.New("search handler needs a search engine")

// SearchEngine finds the books that match a query. Implementations must be safe for concurrent
// use, because net/http serves every request on its own goroutine.
type SearchEngine interface {
	Search(ctx context.Context, query SearchQuery) (SearchPage, error)
}

// SearchQuery is a validated search request: Term is never empty, Offset is never negative
// and Limit is always between 1 and MaxLimit.
type SearchQuery struct {
	Term    string
	Filters SearchFilters
	Offset  int
	Limit   int
}

// SearchFilters narrows a keyword search; filters combine with AND. An empty string or a zero
// Year means "no filter", and omitempty drops those fields from the JSON echo of the request.
type SearchFilters struct {
	Author   string `json:"author,omitempty" example:"Jane Austen"`
	Language string `json:"language,omitempty" example:"en"`
	Year     int    `json:"year,omitempty" example:"1813"`
}

// SearchPage is one page of matches. Total counts every match, not only the books in Books.
type SearchPage struct {
	Total int
	Books []models.BookMetadata
}

// SearchResponse is the body of a successful GET /search.
type SearchResponse struct {
	// Keyword that was searched, without surrounding spaces
	Query string `json:"query" example:"adventure"`
	// Filters that were applied; absent filters are omitted
	Filters SearchFilters `json:"filters"`
	// Number of matches across all pages
	Total  int `json:"total" example:"25"`
	Offset int `json:"offset" example:"0"`
	Limit  int `json:"limit" example:"20"`
	// Books of the requested page; never null
	Results []BookResult `json:"results"`
}

// BookResult is one book of a search response.
type BookResult struct {
	BookID models.BookID `json:"book_id" swaggertype:"integer" example:"1342"`
	Title  string        `json:"title" example:"Pride and Prejudice"`
	Author string        `json:"author" example:"Jane Austen"`
	// ISO 639 code; books written in several languages list them comma-separated ("en,fr")
	Language string `json:"language" example:"en"`
	// Original publication year, or 0 when unknown
	Year int `json:"year" example:"1813"`
}

// SearchHandler serves GET /search on top of a SearchEngine.
type SearchHandler struct {
	engine SearchEngine
}

// NewSearchHandler returns a handler that answers searches with the given engine.
func NewSearchHandler(engine SearchEngine) (SearchHandler, error) {
	if engine == nil {
		return SearchHandler{}, ErrMissingEngine
	}
	return SearchHandler{engine: engine}, nil
}

// Search answers one keyword search with one page of matching books.
//
//	@Summary		Search books by keyword
//	@Description	Returns one page of the books that contain the keyword `q`, optionally narrowed by author,
//	@Description	language and original publication year. Filters can be combined and are joined with AND.
//	@Description	Empty filter values (for example `author=`) are treated as absent.
//	@Description	Only the filters that were applied are echoed in `filters`; the object is `{}` when none were sent.
//	@Description	Results are ordered by relevance, most relevant first, with ties broken by ascending `book_id`.
//	@Description	`total` counts every match, while `results` holds at most `limit` books starting at `offset`.
//	@Description	A valid query without matches, or an `offset` past the last match, returns 200 with `results: []`.
//	@Tags			search
//	@Produce		json
//	@Param			q			query		string			true	"Keyword to search for; surrounding spaces are ignored"	minlength(1)	example(adventure)
//	@Param			author		query		string			false	"Case-insensitive partial match on the author name"	example(Austen)
//	@Param			language	query		string			false	"ISO 639 language code, case-insensitive exact match (en, es, fr...)"	example(en)
//	@Param			year		query		int				false	"Exact original publication year"	minimum(1)	maximum(9999)	example(1813)
//	@Param			offset		query		int				false	"Number of matches to skip before the first result"	default(0)	minimum(0)
//	@Param			limit		query		int				false	"Maximum number of results in the page"	default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	SearchResponse	"One page of matching books"
//	@Failure		400			{object}	ErrorResponse	"q is missing (missing_parameter) or a parameter is malformed (invalid_parameter)"
//	@Failure		500			{object}	ErrorResponse	"The search engine failed (internal_error)"
//	@x-provider		"Search Service"
//	@x-consumers	["Front-end"]
//	@x-audience		"front-end"
//	@x-idempotent	true
//	@x-completion	"synchronous"
//	@Router			/search [get]
func (handler SearchHandler) Search(writer http.ResponseWriter, request *http.Request) {
	query, err := parseSearchQuery(request.URL.Query())
	if err != nil {
		writeQueryError(writer, err)
		return
	}
	page, err := handler.engine.Search(request.Context(), query)
	if err != nil {
		writeEngineFailure(writer, query, err)
		return
	}
	writeJSON(writer, http.StatusOK, newSearchResponse(query, page))
}

// queryParameterError describes a query parameter that breaks the API contract.
type queryParameterError struct {
	code    string
	message string
}

func (err queryParameterError) Error() string {
	return err.message
}

func missingParameter(name string) queryParameterError {
	return queryParameterError{code: missingParameterCode, message: fmt.Sprintf("query parameter %q is required", name)}
}

func invalidParameter(name, rule string) queryParameterError {
	return queryParameterError{code: invalidParameterCode, message: fmt.Sprintf("query parameter %q %s", name, rule)}
}

// integerParameter is the contract of one optional integer query parameter.
type integerParameter struct {
	name     string
	fallback int
	minimum  int
	maximum  int
	rule     string
}

var (
	yearParameter   = integerParameter{name: "year", minimum: 1, maximum: 9999, rule: "must be a year between 1 and 9999"}
	offsetParameter = integerParameter{name: "offset", minimum: 0, maximum: math.MaxInt32, rule: "must be a non-negative integer"}
	limitParameter  = integerParameter{
		name: "limit", fallback: DefaultLimit, minimum: 1, maximum: MaxLimit,
		rule: fmt.Sprintf("must be an integer between 1 and %d", MaxLimit),
	}
)

// parse reads the parameter, returning its fallback when the request does not send it.
func (parameter integerParameter) parse(values url.Values) (int, error) {
	raw := strings.TrimSpace(values.Get(parameter.name))
	if raw == "" {
		return parameter.fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < parameter.minimum || value > parameter.maximum {
		return 0, invalidParameter(parameter.name, parameter.rule)
	}
	return value, nil
}

// parseSearchQuery validates the query string of GET /search. Every error it returns is a
// queryParameterError.
func parseSearchQuery(values url.Values) (SearchQuery, error) {
	query := SearchQuery{Term: strings.TrimSpace(values.Get("q"))}
	if query.Term == "" {
		return SearchQuery{}, missingParameter("q")
	}
	var err error
	if query.Filters, err = parseFilters(values); err != nil {
		return SearchQuery{}, err
	}
	if query.Offset, err = offsetParameter.parse(values); err != nil {
		return SearchQuery{}, err
	}
	if query.Limit, err = limitParameter.parse(values); err != nil {
		return SearchQuery{}, err
	}
	return query, nil
}

// parseFilters reads the optional filters. Language codes are lower-cased to match the
// ISO 639 codes the indexer stores ("EN" -> "en").
func parseFilters(values url.Values) (SearchFilters, error) {
	year, err := yearParameter.parse(values)
	if err != nil {
		return SearchFilters{}, err
	}
	return SearchFilters{
		Author:   strings.TrimSpace(values.Get("author")),
		Language: strings.ToLower(strings.TrimSpace(values.Get("language"))),
		Year:     year,
	}, nil
}

func newSearchResponse(query SearchQuery, page SearchPage) SearchResponse {
	results := make([]BookResult, 0, len(page.Books))
	for _, book := range page.Books {
		results = append(results, newBookResult(book))
	}
	return SearchResponse{
		Query:   query.Term,
		Filters: query.Filters,
		Total:   page.Total,
		Offset:  query.Offset,
		Limit:   query.Limit,
		Results: results,
	}
}

func newBookResult(book models.BookMetadata) BookResult {
	return BookResult{
		BookID:   book.BookID,
		Title:    book.Title,
		Author:   book.Author,
		Language: book.Language,
		Year:     book.PublicationYear,
	}
}

// writeQueryError answers 400 Bad Request with the code of the broken parameter rule.
func writeQueryError(writer http.ResponseWriter, err error) {
	response := ErrorResponse{Error: invalidParameterCode, Message: err.Error()}
	var parameterErr queryParameterError
	if errors.As(err, &parameterErr) {
		response.Error = parameterErr.code
	}
	writeJSON(writer, http.StatusBadRequest, response)
}

// writeEngineFailure logs the engine error and answers 500 without exposing internal details.
func writeEngineFailure(writer http.ResponseWriter, query SearchQuery, err error) {
	slog.Error("search engine failed", "term", query.Term, "filters", query.Filters, "error", err)
	writeJSON(writer, http.StatusInternalServerError, ErrorResponse{
		Error:   internalErrorCode,
		Message: "the search could not be completed, please try again later",
	})
}
