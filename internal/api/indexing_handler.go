package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"boogle-engine/internal/datalake"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

// Error codes returned in ErrorResponse.Error by the indexing endpoints. invalid_book_id,
// book_not_found and internal_error are shared with the ingestion endpoints.
const (
	rebuildInProgressCode  = "rebuild_in_progress"
	rebuildInterruptedCode = "rebuild_interrupted"
)

const bytesPerMegabyte = 1 << 20

// ErrMissingIndex is returned by NewIndexingHandler when it receives no BookIndex.
var ErrMissingIndex = errors.New("indexing handler needs a book index")

// BookIndex is the searchable index that the Indexing API maintains. The service wires the
// MongoDB datamart adapter to it.
type BookIndex interface {
	IndexBook(ctx context.Context, bookID models.BookID) error
	Rebuild(ctx context.Context) (indexer.RebuildReport, error)
	Stats() indexer.IndexStats
}

// IndexOutcome is the result reported for one book by POST /index/update/{book_id}.
type IndexOutcome string

// IndexUpdated means the book is in the published index.
const IndexUpdated IndexOutcome = "updated"

// IndexUpdateResponse is the body of a successful POST /index/update/{book_id}.
type IndexUpdateResponse struct {
	BookID models.BookID `json:"book_id" swaggertype:"integer" example:"1342"`
	Index  IndexOutcome  `json:"index" swaggertype:"string" enums:"updated" example:"updated"`
}

// RebuildResponse is the body of a successful POST /index/rebuild.
type RebuildResponse struct {
	// Books of the datalake that the published index was rebuilt from
	BooksProcessed int `json:"books_processed" example:"1000"`
	// Duration of the rebuild in Go duration syntax (time.ParseDuration), rounded to milliseconds
	ElapsedTime string `json:"elapsed_time" example:"35.2s"`
}

// IndexStatusResponse is the body of GET /index/status.
type IndexStatusResponse struct {
	// Books in the published metadata catalog
	BooksIndexed int `json:"books_indexed" example:"1200"`
	// UTC time of the last successful update or rebuild; at startup, when the empty index was published
	LastUpdate time.Time `json:"last_update" swaggertype:"string" format:"date-time" example:"2025-10-08T14:05:00Z"`
	// Rough estimate in MiB: size of the stored index data (datamart document bytes)
	IndexSizeMB float64 `json:"index_size_MB" example:"42.7"`
}

// IndexingHandler serves the internal Indexing API that the Control Module consumes. Its update
// slots bound how many POST /index/update requests may be indexed at the same time, so a burst of
// requests waits instead of exhausting the datamart connection pool.
type IndexingHandler struct {
	index          BookIndex
	rebuildTimeout time.Duration
	updateSlots    chan struct{}
}

// NewIndexingHandler returns a handler over index whose rebuilds may run for at most
// rebuildTimeout and which indexes at most maxConcurrentUpdates books at a time.
func NewIndexingHandler(index BookIndex, rebuildTimeout time.Duration, maxConcurrentUpdates int) (IndexingHandler, error) {
	if index == nil {
		return IndexingHandler{}, ErrMissingIndex
	}
	if rebuildTimeout <= 0 {
		return IndexingHandler{}, fmt.Errorf("rebuild timeout must be positive, got %s", rebuildTimeout)
	}
	if maxConcurrentUpdates < 1 {
		return IndexingHandler{}, fmt.Errorf("max concurrent updates must be a positive integer, got %d", maxConcurrentUpdates)
	}
	return IndexingHandler{
		index:          index,
		rebuildTimeout: rebuildTimeout,
		updateSlots:    make(chan struct{}, maxConcurrentUpdates),
	}, nil
}

// RegisterRoutes mounts the indexing endpoints on router. The Go 1.22 method patterns make any
// other method on these paths answer 405 Method Not Allowed.
func (handler IndexingHandler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /index/update/{book_id}", handler.UpdateBook)
	router.HandleFunc("POST /index/rebuild", handler.Rebuild)
	router.HandleFunc("GET /index/status", handler.Status)
}

// UpdateBook indexes one book of the datalake into the published index.
//
//	@Summary		Index one book
//	@Description	- **Provider:** Indexing Service
//	@Description	- **Intended consumer:** Control Module (internal API, not exposed to the front-end)
//	@Description	- **Position in the workflow:** second step of a book's lifecycle. Call it once POST /ingest/{book_id} has returned
//	@Description	200 (or GET /ingest/status/{book_id} answers `available`); the datalake only exposes completely written books.
//	@Description
//	@Description	Reads the book from the datalake, extracts its metadata (title, author, language, publication and release years)
//	@Description	and adds its body terms to the inverted index. The request has no body.
//	@Description
//	@Description	**Completion and publication.** Synchronous: a 200 response means the work has fully completed and the book's
//	@Description	metadata record and postings are in the published index, visible to every request that starts afterwards. If a
//	@Description	rebuild is running, the book is recorded and replayed on the rebuilt index right after it is published, so the
//	@Description	rebuild cannot lose it.
//	@Description
//	@Description	**Idempotency and consistency.** Safe to repeat: re-indexing a book that is already indexed replaces its metadata
//	@Description	record and its postings (terms the book no longer contains stop returning it), so the index ends up exactly as
//	@Description	after the first call. Updates of different books run in parallel, but the endpoint indexes at most
//	@Description	`-max-concurrent-updates` books at once (8 by default) and queues the rest until a slot frees; concurrent
//	@Description	updates of the same book are harmless.
//	@Description
//	@Description	**Timeouts and retries expected of the caller.** One update reads one book from local disk and usually takes
//	@Description	milliseconds (about a second for the largest books); a client timeout of 30 s is ample and an update is never
//	@Description	blocked by a running rebuild. A caller that disconnects cancels its own update only.
//	@Description	- Client timeout, connection error or `500 internal_error`: retry the same request with exponential backoff.
//	@Description	- `404 book_not_found`: the book is not completely ingested. Ingest it first, then retry; repeating alone will not help.
//	@Description	- The index is stored in the MongoDB datamart, so indexed books stay searchable across service restarts.
//	@Tags			indexing
//	@Produce		json
//	@Param			book_id	path		int					true	"Project Gutenberg book ID (positive integer)"	minimum(1)	example(1342)
//	@Success		200		{object}	IndexUpdateResponse	"The book is in the published index"
//	@Failure		400		{object}	ErrorResponse		"invalid_book_id: book_id is not a positive integer"
//	@Failure		404		{object}	ErrorResponse		"book_not_found: the datalake holds no complete copy of the book"
//	@Failure		500		{object}	ErrorResponse		"internal_error: the book could not be read or indexed"
//	@x-provider		"Indexing Service"
//	@x-consumers	["Control Module"]
//	@x-audience		"internal"
//	@x-idempotent	true
//	@x-completion	"synchronous"
//	@Router			/index/update/{book_id} [post]
func (handler IndexingHandler) UpdateBook(writer http.ResponseWriter, request *http.Request) {
	bookID, err := parseBookIDPath(request)
	if err != nil {
		writeInvalidBookID(writer, err)
		return
	}
	if err := handler.acquireUpdateSlot(request.Context()); err != nil {
		slog.Warn("[INDEXING] update gave up waiting for a slot", "book_id", bookID, "error", err)
		message := fmt.Sprintf("book %d could not be indexed: the service was saturated and the caller stopped waiting", bookID)
		writeJSON(writer, http.StatusInternalServerError, ErrorResponse{Error: internalErrorCode, Message: message})
		return
	}
	defer handler.releaseUpdateSlot()
	if err := handler.index.IndexBook(request.Context(), bookID); err != nil {
		writeIndexingFailure(writer, bookID, err)
		return
	}
	writeJSON(writer, http.StatusOK, IndexUpdateResponse{BookID: bookID, Index: IndexUpdated})
}

// acquireUpdateSlot takes one of the handler's update slots, waiting while every slot is busy
// until the caller's context ends: a client that disconnects while queued frees nothing and is
// never charged a slot.
func (handler IndexingHandler) acquireUpdateSlot(ctx context.Context) error {
	select {
	case handler.updateSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for an update slot: %w", ctx.Err())
	}
}

func (handler IndexingHandler) releaseUpdateSlot() {
	<-handler.updateSlots
}

// Rebuild replaces the published index with one built from scratch out of the whole datalake.
//
//	@Summary		Rebuild the whole index from the datalake
//	@Description	- **Provider:** Indexing Service
//	@Description	- **Intended consumer:** Control Module (internal, administrative): after the service restarts, or to re-index
//	@Description	every book after the tokenizer or the metadata parser changes
//	@Description
//	@Description	Builds a brand-new metadata catalog and inverted index from every complete book in the datalake, written to
//	@Description	fresh datamart collections. Books still being ingested are not included. The request has no body.
//	@Description
//	@Description	**Completion and publication.** Synchronous and atomic. The new index is built aside while the previous one keeps
//	@Description	answering, and replaces it in a single step once every book is indexed: a 200 response means the new index is
//	@Description	published. If the rebuild fails or is interrupted, it is discarded and the previous index stays published
//	@Description	unchanged; a half-built index is never visible.
//	@Description
//	@Description	**Idempotency and consistency.** Repeating it over the same datalake produces the same index. Only one rebuild runs
//	@Description	at a time across the service replicas (a lease in the datamart serializes them): another request meanwhile gets
//	@Description	`409 rebuild_in_progress`. POST /index/update calls during a rebuild are not blocked: they are recorded and replayed
//	@Description	on the rebuilt index after the swap, so the published result never loses them.
//	@Description
//	@Description	**Timeouts and retries expected of the caller.** The duration grows with the datalake (the assignment's reference
//	@Description	is about 35 s per 1000 books) and is capped by the service's rebuild budget (`-rebuild-timeout`, 10 minutes by
//	@Description	default). Keep the connection open with a client timeout above that budget: disconnecting abandons the rebuild.
//	@Description	- `409 rebuild_in_progress`: do not start another one; wait and poll GET /index/status until `last_update` changes.
//	@Description	- `503 rebuild_interrupted` and `500 internal_error`: the previous index is still published; retry later with backoff.
//	@Tags			indexing
//	@Produce		json
//	@Success		200	{object}	RebuildResponse	"The rebuilt index is published"
//	@Failure		409	{object}	ErrorResponse	"rebuild_in_progress: another rebuild is running"
//	@Failure		500	{object}	ErrorResponse	"internal_error: the datalake could not be listed or a book could not be indexed"
//	@Failure		503	{object}	ErrorResponse	"rebuild_interrupted: the rebuild budget ran out or the caller disconnected"
//	@x-provider		"Indexing Service"
//	@x-consumers	["Control Module"]
//	@x-audience		"internal"
//	@x-idempotent	true
//	@x-completion	"synchronous"
//	@Router			/index/rebuild [post]
func (handler IndexingHandler) Rebuild(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), handler.rebuildTimeout)
	defer cancel()
	report, err := handler.index.Rebuild(ctx)
	if err != nil {
		writeRebuildFailure(writer, err)
		return
	}
	slog.Info("[INDEXING] index rebuilt", "books", report.BooksProcessed, "elapsed", report.Elapsed)
	writeJSON(writer, http.StatusOK, RebuildResponse{
		BooksProcessed: report.BooksProcessed,
		ElapsedTime:    report.Elapsed.Round(time.Millisecond).String(),
	})
}

// Status reports statistics about the published index.
//
//	@Summary		Report indexing statistics
//	@Description	- **Provider:** Indexing Service
//	@Description	- **Intended consumer:** Control Module (internal, operational): to follow indexing progress, detect the end of a
//	@Description	rebuild and record memory use during benchmarks
//	@Description
//	@Description	**Fields.** `books_indexed` counts the published books of the metadata datamart. `last_update` is the UTC time of the
//	@Description	last successful update or rebuild (at startup, the time the adapter was created). `index_size_MB` is a rough
//	@Description	estimate of the stored index data: the document bytes of the MongoDB datamart.
//	@Description
//	@Description	**Consistency.** Describes the published index only; a running rebuild shows up when it is published.
//	@Description
//	@Description	**Idempotency, timeouts and retries.** Read-only, cheap and safe to poll at any time; a client timeout of about
//	@Description	5 s is enough. Retry on timeouts and connection errors.
//	@Tags			indexing
//	@Produce		json
//	@Success		200	{object}	IndexStatusResponse	"Statistics of the published index"
//	@x-provider		"Indexing Service"
//	@x-consumers	["Control Module"]
//	@x-audience		"internal"
//	@x-idempotent	true
//	@x-completion	"synchronous"
//	@Router			/index/status [get]
func (handler IndexingHandler) Status(writer http.ResponseWriter, _ *http.Request) {
	stats := handler.index.Stats()
	writeJSON(writer, http.StatusOK, IndexStatusResponse{
		BooksIndexed: stats.BooksIndexed,
		LastUpdate:   stats.LastUpdate.UTC().Truncate(time.Second),
		IndexSizeMB:  megabytesWithOneDecimal(stats.SizeBytes),
	})
}

// writeIndexingFailure answers 404 when the book is not in the datalake and 500 otherwise.
func writeIndexingFailure(writer http.ResponseWriter, bookID models.BookID, err error) {
	if errors.Is(err, datalake.ErrBookNotFound) {
		message := fmt.Sprintf("book %d is not in the datalake; ingest it before indexing it", bookID)
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: bookNotFoundCode, Message: message})
		return
	}
	slog.Error("[INDEXING] book could not be indexed", "book_id", bookID, "error", err)
	message := fmt.Sprintf("book %d could not be indexed", bookID)
	writeJSON(writer, http.StatusInternalServerError, ErrorResponse{Error: internalErrorCode, Message: message})
}

// writeRebuildFailure maps a rebuild error; in every case the previous index stays published.
func writeRebuildFailure(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, indexer.ErrRebuildInProgress):
		writeJSON(writer, http.StatusConflict, ErrorResponse{Error: rebuildInProgressCode, Message: err.Error()})
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		slog.Warn("[INDEXING] rebuild interrupted", "error", err)
		message := "the rebuild was interrupted before it finished; the previous index is still published"
		writeJSON(writer, http.StatusServiceUnavailable, ErrorResponse{Error: rebuildInterruptedCode, Message: message})
	default:
		slog.Error("[INDEXING] rebuild failed", "error", err)
		message := "the rebuild failed; the previous index is still published"
		writeJSON(writer, http.StatusInternalServerError, ErrorResponse{Error: internalErrorCode, Message: message})
	}
}

func megabytesWithOneDecimal(sizeBytes uint64) float64 {
	return math.Round(float64(sizeBytes)/bytesPerMegabyte*10) / 10
}
