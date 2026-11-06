package ingestionapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"boogle-engine/internal/api"
	"boogle-engine/internal/ingestion"
	"boogle-engine/internal/models"
)

const (
	bookUnavailableCode = "book_unavailable"
	downloadFailedCode  = "download_failed"
)

var ErrMissingIngestionDependency = errors.New("ingestion handler needs an ingester and a datalake")

type IngestionResponse struct {
	BookID models.BookID `json:"book_id" swaggertype:"integer" example:"1342"`
	// downloaded: this call wrote the files; available: the datalake already held them
	Status models.IngestionStatus `json:"status" swaggertype:"string" enums:"downloaded,available" example:"downloaded"`
	// Folder of the book in the datalake, always written with forward slashes
	Path string `json:"path" example:"datalake/batch_1000_1999/1342"`
}

type BookStatusResponse struct {
	BookID models.BookID          `json:"book_id" swaggertype:"integer" example:"1342"`
	Status models.IngestionStatus `json:"status" swaggertype:"string" enums:"available" example:"available"`
}

type BookListResponse struct {
	// Number of complete books in the datalake
	Count int `json:"count" example:"3"`
	// IDs of the complete books in ascending order; never null
	Books []models.BookID `json:"books" swaggertype:"array,integer" example:"5,17,1342"`
}

type IngestionHandler struct {
	ingester ingestion.BookIngester
	lake     ingestion.DatalakeReader
}

func NewIngestionHandler(ingester ingestion.BookIngester, lake ingestion.DatalakeReader) (IngestionHandler, error) {
	if ingester == nil || lake == nil {
		return IngestionHandler{}, ErrMissingIngestionDependency
	}
	return IngestionHandler{ingester: ingester, lake: lake}, nil
}

func (handler IngestionHandler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /ingest/{book_id}", handler.Ingest)
	router.HandleFunc("GET /ingest/status/{book_id}", handler.Status)
	router.HandleFunc("GET /ingest/list", handler.List)
}

// @Summary		Ingest a book into the datalake
// @Description	- **Provider:** Ingestion Service
// @Description	- **Intended consumer:** Control Module (internal API, not exposed to the front-end)
// @Description	- **Position in the workflow:** first step of a book's lifecycle; index a book only after this call returns 200
// @Description
// @Description	Downloads book `book_id` from Project Gutenberg, splits it into header and body and stores both files in the
// @Description	datalake folder returned in `path`. The request has no body.
// @Description
// @Description	**Completion semantics.** Synchronous: a 200 response means the work has fully completed. Each file is written
// @Description	atomically (temporary file, flush to disk, rename), so the book is complete and immediately eligible for indexing,
// @Description	and no reader ever sees a partial book. `status` is `downloaded` when this call wrote the files and `available`
// @Description	when the datalake already held them; both mean success.
// @Description
// @Description	**Idempotency.** Safe to repeat; it never duplicates work or data. A duplicate call returns `available` with the
// @Description	same `path` without downloading again, and concurrent calls for the same book share one single download and all
// @Description	receive its result. This coordination lives inside one service process: two replicas may download the same book
// @Description	at once, and the atomic rename still leaves exactly one complete copy.
// @Description
// @Description	**Timeouts and retries expected of the caller.**
// @Description	- The service retries transient download errors by itself within an ingestion budget (`-ingest-timeout`, 2 minutes
// @Description	by default) that also covers waiting for a free download slot. Use a client timeout above it, for example 150 s.
// @Description	- `503 download_failed` and `500 internal_error`: retry the same request later with exponential backoff.
// @Description	- `404 book_unavailable`: permanent. Do not retry; record the book as unavailable.
// @Description	- Client timeout or lost connection: retry the same request. The ingestion keeps running in the service, so the
// @Description	retry joins it or finds the book `available`.
// @Tags			ingestion
// @Produce		json
// @Param			book_id	path		int					true	"Project Gutenberg book ID (positive integer)"	minimum(1)	example(1342)
// @Success		200		{object}	IngestionResponse	"The book is complete in the datalake"
// @Failure		400		{object}	api.ErrorResponse	"invalid_book_id: book_id is not a positive integer"
// @Failure		404		{object}	api.ErrorResponse	"book_unavailable: Gutenberg has no such book or its text lacks the START/END markers"
// @Failure		500		{object}	api.ErrorResponse	"internal_error: the datalake could not be written"
// @Failure		503		{object}	api.ErrorResponse	"download_failed: Gutenberg failed transiently or the ingestion budget ran out"
// @x-provider		"Ingestion Service"
// @x-consumers	["Control Module"]
// @x-audience		"internal"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/ingest/{book_id} [post]
func (handler IngestionHandler) Ingest(writer http.ResponseWriter, request *http.Request) {
	bookID, err := api.ParseBookIDPath(request)
	if err != nil {
		api.WriteInvalidBookID(writer, err)
		return
	}
	result, err := handler.ingester.Ingest(request.Context(), bookID)
	if err != nil {
		slog.Warn("[INGESTION] caller left before the ingestion finished", "book_id", bookID, "error", err)
		return
	}
	writeIngestionResult(writer, result)
}

// @Summary		Check whether a book is in the datalake
// @Description	- **Provider:** Ingestion Service
// @Description	- **Intended consumer:** Control Module (internal API), as the readiness check before a book is indexed
// @Description
// @Description	Answers 200 `available` only when both the header and the body of the book are completely written. Because files
// @Description	are written atomically, a book whose ingestion is still running answers 404 until its POST /ingest/{book_id}
// @Description	has completed.
// @Description
// @Description	**Idempotency, timeouts and retries.** Read-only and safe to poll or repeat at any time. It only checks two files
// @Description	on disk, so a short client timeout (about 5 s) is enough; retry on timeouts and connection errors.
// @Tags			ingestion
// @Produce		json
// @Param			book_id	path		int					true	"Project Gutenberg book ID (positive integer)"	minimum(1)	example(1342)
// @Success		200		{object}	BookStatusResponse	"Both files of the book are in the datalake"
// @Failure		400		{object}	api.ErrorResponse	"invalid_book_id: book_id is not a positive integer"
// @Failure		404		{object}	api.ErrorResponse	"book_not_found: the datalake holds no complete copy of the book"
// @x-provider		"Ingestion Service"
// @x-consumers	["Control Module"]
// @x-audience		"internal"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/ingest/status/{book_id} [get]
func (handler IngestionHandler) Status(writer http.ResponseWriter, request *http.Request) {
	bookID, err := api.ParseBookIDPath(request)
	if err != nil {
		api.WriteInvalidBookID(writer, err)
		return
	}
	if _, err := handler.lake.Locate(bookID); err == nil {
		api.WriteJSON(writer, http.StatusOK, BookStatusResponse{BookID: bookID, Status: models.StatusAvailable})
		return
	}
	message := fmt.Sprintf("book %d is not in the datalake", bookID)
	api.WriteJSON(writer, http.StatusNotFound, api.ErrorResponse{Error: api.BookNotFoundCode, Message: message})
}

// @Summary		List the books in the datalake
// @Description	- **Provider:** Ingestion Service
// @Description	- **Intended consumer:** Control Module (internal API), to pick books not processed yet and to rebuild its work
// @Description	queue after a restart
// @Description
// @Description	Returns the IDs of the books whose header and body are both complete, in ascending order, and their count. Books
// @Description	still being ingested are not listed until their POST /ingest/{book_id} has completed. The list is a snapshot taken
// @Description	while scanning the datalake folders and is not paginated.
// @Description
// @Description	**Idempotency, timeouts and retries.** Read-only and safe to repeat. The scan grows with the number of stored books,
// @Description	so allow a generous client timeout (about 30 s) and retry `500 internal_error` with exponential backoff.
// @Tags			ingestion
// @Produce		json
// @Success		200	{object}	BookListResponse	"Complete books in ascending order"
// @Failure		500	{object}	api.ErrorResponse	"internal_error: the datalake folders could not be read"
// @x-provider		"Ingestion Service"
// @x-consumers	["Control Module"]
// @x-audience		"internal"
// @x-idempotent	true
// @x-completion	"synchronous"
// @Router			/ingest/list [get]
func (handler IngestionHandler) List(writer http.ResponseWriter, _ *http.Request) {
	bookIDs, err := handler.lake.ListBookIDs()
	if err != nil {
		slog.Error("[INGESTION] datalake could not be listed", "error", err)
		api.WriteJSON(writer, http.StatusInternalServerError, api.ErrorResponse{Error: api.InternalErrorCode, Message: "the datalake could not be listed"})
		return
	}
	books := append([]models.BookID{}, bookIDs...)
	api.WriteJSON(writer, http.StatusOK, BookListResponse{Count: len(books), Books: books})
}

func writeIngestionResult(writer http.ResponseWriter, result models.IngestionResult) {
	switch result.Status {
	case models.StatusDownloaded, models.StatusAvailable:
		api.WriteJSON(writer, http.StatusOK, newIngestionResponse(result))
	case models.StatusUnavailable:
		slog.Warn("[INGESTION] book unavailable", "book_id", result.BookID, "error", result.Err)
		api.WriteJSON(writer, http.StatusNotFound, api.ErrorResponse{Error: bookUnavailableCode, Message: result.Err.Error()})
	default:
		writeIngestionFailure(writer, result)
	}
}

func newIngestionResponse(result models.IngestionResult) IngestionResponse {
	return IngestionResponse{BookID: result.BookID, Status: result.Status, Path: filepath.ToSlash(result.Path)}
}

func writeIngestionFailure(writer http.ResponseWriter, result models.IngestionResult) {
	if errors.Is(result.Err, models.ErrTransient) {
		slog.Warn("[INGESTION] download failed", "book_id", result.BookID, "error", result.Err)
		api.WriteJSON(writer, http.StatusServiceUnavailable, api.ErrorResponse{Error: downloadFailedCode, Message: result.Err.Error()})
		return
	}
	slog.Error("[INGESTION] book could not be stored", "book_id", result.BookID, "status", result.Status, "error", result.Err)
	message := fmt.Sprintf("book %d could not be stored in the datalake", result.BookID)
	api.WriteJSON(writer, http.StatusInternalServerError, api.ErrorResponse{Error: api.InternalErrorCode, Message: message})
}
