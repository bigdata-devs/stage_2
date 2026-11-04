package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"boogle-engine/internal/search"
)

var ErrMissingCatalog = errors.New("catalog handler needs a book catalog")

type FilterValuesResponse struct {
	// ISO 639 language codes of the published books, sorted ascending; never null
	Languages []string `json:"languages" swaggertype:"array,string" example:"en,es,fr"`
	// Original publication years of the published books, sorted ascending; never null
	Years []int `json:"years" swaggertype:"array,integer" example:"1719,1813,1851"`
}

type CatalogHandler struct {
	catalog search.BookCatalog
}

func NewCatalogHandler(catalog search.BookCatalog) (CatalogHandler, error) {
	if catalog == nil {
		return CatalogHandler{}, ErrMissingCatalog
	}
	return CatalogHandler{catalog: catalog}, nil
}

func (handler CatalogHandler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /books/{book_id}", handler.Book)
	router.HandleFunc("GET /search/filters", handler.Filters)
}

// @Summary		Get one book by ID
// @Description	- **Provider:** Search Service
// @Description	- **Intended consumer:** Front-end, to render the details of a book chosen from a search result.
// @Description	- **Position in the workflow:** optional detail view after GET /search; it is one of the
// @Description	variants of the front-end contract, not a separate route family.
// @Description
// @Description	Returns the metadata of a published book. A book that does not exist, or that is indexed
// @Description	but still awaiting publication, is not visible and answers 404. The request has no body.
// @Description
// @Description	**Completion and publication.** Synchronous: the response always reflects the published
// @Description	state of the datamart at the moment it is served.
// @Description
// @Description	**Idempotency and retries.** Read-only; repeating the request is always safe. A client
// @Description	timeout of about 5 s is enough. Retry on timeouts and connection errors; a 404 does not
// @Description	become 200 until the book is ingested and indexed.
// @Tags			search
// @Produce		json
// @Param			book_id	path		int			true	"Project Gutenberg book ID (positive integer)"	minimum(1)	example(1342)
// @Success		200		{object}	BookResult	"The metadata of the published book"
// @Failure		400		{object}	ErrorResponse	"invalid_book_id: book_id is not a positive integer"
// @Failure		404		{object}	ErrorResponse	"book_not_found: no published book with that ID"
// @Failure		500		{object}	ErrorResponse	"internal_error: the datamart could not be read"
// @x-provider		"Search Service"
// @x-consumers	["Front-end"]
// @x-audience		"front-end"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/books/{book_id} [get]
func (handler CatalogHandler) Book(writer http.ResponseWriter, request *http.Request) {
	bookID, err := parseBookIDPath(request)
	if err != nil {
		writeInvalidBookID(writer, err)
		return
	}
	book, found, err := handler.catalog.PublishedBook(request.Context(), bookID)
	if err != nil {
		writeCatalogFailure(writer, "look up a book", err)
		return
	}
	if !found {
		message := fmt.Sprintf("no published book with ID %d", bookID)
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: bookNotFoundCode, Message: message})
		return
	}
	writeJSON(writer, http.StatusOK, newBookResult(book))
}

// @Summary		List the available search filters
// @Description	- **Provider:** Search Service
// @Description	- **Intended consumer:** Front-end, to populate the language and year choices of its search form.
// @Description
// @Description	Returns the distinct ISO 639 language codes (a book written in several languages contributes
// @Description	every one of its codes) and the distinct original publication years of the published books,
// @Description	both sorted ascending. Books without a known language or year do not contribute a value, so
// @Description	every returned value can be sent back to GET /search and actually match results. The request
// @Description	has no body; the slices are empty arrays, never null, when no books are published.
// @Description
// @Description	**Completion and publication.** Synchronous and read-only; it reflects the published state
// @Description	of the datamart when it is served.
// @Description
// @Description	**Idempotency and retries.** Safe to repeat; retry on timeouts and connection errors.
// @Tags			search
// @Produce		json
// @Success		200				{object}	FilterValuesResponse	"Distinct filter values of the published books"
// @Failure		500				{object}	ErrorResponse			"internal_error: the datamart could not be read"
// @x-provider		"Search Service"
// @x-consumers	["Front-end"]
// @x-audience		"front-end"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/search/filters [get]
func (handler CatalogHandler) Filters(writer http.ResponseWriter, request *http.Request) {
	values, err := handler.catalog.FilterOptions(request.Context())
	if err != nil {
		writeCatalogFailure(writer, "list filter values", err)
		return
	}
	response := FilterValuesResponse{Languages: values.Languages, Years: values.Years}
	if response.Languages == nil {
		response.Languages = []string{}
	}
	if response.Years == nil {
		response.Years = []int{}
	}
	writeJSON(writer, http.StatusOK, response)
}

func writeCatalogFailure(writer http.ResponseWriter, action string, err error) {
	slog.Error("catalog request failed", "action", action, "error", err)
	writeJSON(writer, http.StatusInternalServerError, ErrorResponse{
		Error:   internalErrorCode,
		Message: "the book catalog could not be read, please try again later",
	})
}
