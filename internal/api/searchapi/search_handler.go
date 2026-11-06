package searchapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"boogle-engine/internal/api"
	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

const (
	missingParameterCode = "missing_parameter"
	invalidParameterCode = "invalid_parameter"
)

var ErrMissingSearcher = errors.New("search handler needs a searcher")

var searchParameters = map[search.Field]string{
	search.FieldText:   "q",
	search.FieldYear:   "year",
	search.FieldOffset: "offset",
	search.FieldLimit:  "limit",
}

type SearchFilters struct {
	Author   string `json:"author,omitempty" example:"Jane Austen"`
	Language string `json:"language,omitempty" example:"en"`
	Year     int    `json:"year,omitempty" example:"1813"`
}

type SearchResponse struct {
	// Keywords exactly as sent in q, without surrounding spaces
	Query string `json:"query" example:"sherlock holmes"`
	// Filters that were applied; absent filters are omitted
	Filters SearchFilters `json:"filters"`
	// Number of matches across all pages
	Total  int `json:"total" example:"25"`
	Offset int `json:"offset" example:"0"`
	Limit  int `json:"limit" example:"20"`
	// Books of the requested page; never null
	Results []BookResult `json:"results"`
}

type BookResult struct {
	BookID models.BookID `json:"book_id" swaggertype:"integer" example:"1342"`
	Title  string        `json:"title" example:"Pride and Prejudice"`
	Author string        `json:"author" example:"Jane Austen"`
	// ISO 639 code; books written in several languages list them comma-separated ("en,fr")
	Language string `json:"language" example:"en"`
	// Original publication year, or 0 when unknown
	Year int `json:"year" example:"1813"`
}

type SearchHandler struct {
	searcher search.Searcher
}

func NewSearchHandler(searcher search.Searcher) (SearchHandler, error) {
	if searcher == nil {
		return SearchHandler{}, ErrMissingSearcher
	}
	return SearchHandler{searcher: searcher}, nil
}

// @Summary		Search books by keywords
// @Description	Returns one page of the books that contain every word of `q`, optionally narrowed by author,
// @Description	language and original publication year. Filters can be combined and are joined with AND.
// @Description	`q` may hold several words, matched with AND: a book is returned only when it contains all of them.
// @Description	Words are split exactly like book text: runs of the letters a-z, case-insensitive, so digits, spaces
// @Description	and punctuation separate words (`Moby-Dick` searches `moby` AND `dick`). Repeated words count once.
// @Description	Stop words (the, and, of...), single letters and Roman numerals are never indexed and are ignored;
// @Description	a `q` made only of them matches nothing and returns 200 with `results: []`.
// @Description	`q` is rejected with 400 `invalid_parameter` when it contains no word at all (for example `1813` or `?!`)
// @Description	or more than 10 distinct search words.
// @Description	Empty filter values (for example `author=`) are treated as absent.
// @Description	Only the filters that were applied are echoed in `filters`; the object is `{}` when none were sent.
// @Description	Results are ordered by relevance, most relevant first: the summed frequency of the query words in
// @Description	the book, with ties broken by ascending `book_id`.
// @Description	`total` counts every match, while `results` holds at most `limit` books starting at `offset`.
// @Description	A valid query without matches, or an `offset` past the last match, returns 200 with `results: []`.
// @Tags			search
// @Produce		json
// @Param			q			query		string			true	"One or more keywords, all of which a book must contain (AND); surrounding spaces are ignored"	minlength(1)	example(sherlock holmes)
// @Param			author		query		string			false	"Case-insensitive partial match on the author name"	example(Austen)
// @Param			language	query		string			false	"ISO 639 language code, case-insensitive exact match (en, es, fr...)"	example(en)
// @Param			year		query		int				false	"Exact original publication year"	minimum(1)	maximum(9999)	example(1813)
// @Param			offset		query		int				false	"Number of matches to skip before the first result"	default(0)	minimum(0)
// @Param			limit		query		int				false	"Maximum number of results in the page"	default(20)	minimum(1)	maximum(100)
// @Success		200			{object}	SearchResponse		"One page of matching books"
// @Failure		400			{object}	api.ErrorResponse	"q is missing (missing_parameter); q has no word or more than 10 distinct words, or another parameter is malformed (invalid_parameter)"
// @Failure		500			{object}	api.ErrorResponse	"The search engine failed (internal_error)"
// @x-provider		"Search Service"
// @x-consumers	["Front-end"]
// @x-audience		"front-end"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/search [get]
func (handler SearchHandler) Search(writer http.ResponseWriter, request *http.Request) {
	searchRequest, err := parseSearchRequest(request.URL.Query())
	if err != nil {
		writeQueryError(writer, err)
		return
	}
	result, err := handler.searcher.Search(request.Context(), searchRequest)
	if errors.Is(err, search.ErrInvalidQuery) {
		writeQueryError(writer, err)
		return
	}
	if err != nil {
		writeSearchFailure(writer, searchRequest, err)
		return
	}
	api.WriteJSON(writer, http.StatusOK, newSearchResponse(result))
}

func parseSearchRequest(values url.Values) (search.Request, error) {
	year, err := optionalInteger(values, search.FieldYear)
	if err != nil {
		return search.Request{}, err
	}
	offset, err := optionalInteger(values, search.FieldOffset)
	if err != nil {
		return search.Request{}, err
	}
	limit, err := optionalInteger(values, search.FieldLimit)
	if err != nil {
		return search.Request{}, err
	}
	return search.Request{
		Text:     values.Get(searchParameters[search.FieldText]),
		Author:   values.Get("author"),
		Language: values.Get("language"),
		Year:     year,
		Offset:   offset,
		Limit:    limit,
	}, nil
}

func optionalInteger(values url.Values, field search.Field) (*int, error) {
	raw := strings.TrimSpace(values.Get(searchParameters[field]))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, search.InvalidNumber(field)
	}
	return &value, nil
}

func newSearchResponse(result search.Result) SearchResponse {
	results := make([]BookResult, 0, len(result.Page.Books))
	for _, book := range result.Page.Books {
		results = append(results, newBookResult(book))
	}
	query := result.Query
	return SearchResponse{
		Query: query.Text,
		Filters: SearchFilters{
			Author:   query.Filters.Author,
			Language: query.Filters.Language,
			Year:     query.Filters.Year,
		},
		Total:   result.Page.Total,
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

func writeQueryError(writer http.ResponseWriter, err error) {
	response := api.ErrorResponse{Error: invalidParameterCode, Message: err.Error()}
	var invalid search.InvalidQueryError
	if errors.As(err, &invalid) {
		name := searchParameters[invalid.Field]
		response.Message = fmt.Sprintf("query parameter %q %s", name, invalid.Rule)
		if invalid.Missing {
			response = api.ErrorResponse{Error: missingParameterCode, Message: fmt.Sprintf("query parameter %q is required", name)}
		}
	}
	api.WriteJSON(writer, http.StatusBadRequest, response)
}

func writeSearchFailure(writer http.ResponseWriter, request search.Request, err error) {
	slog.Error("search failed", "q", request.Text, "author", request.Author, "language", request.Language, "error", err)
	api.WriteJSON(writer, http.StatusInternalServerError, api.ErrorResponse{
		Error:   api.InternalErrorCode,
		Message: "the search could not be completed, please try again later",
	})
}
