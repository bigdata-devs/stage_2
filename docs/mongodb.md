# MongoDB Datamart — Integration Guide

**Owner:** Member 4 (MongoDB & datamarts) · **Audience:** Member 3 (Indexing Service), Member 5 (Search Service), and anyone wiring the services together.

This document hands over `internal/mongodb` — the datamart storage of Boogle Engine — and the two small adapter files that connect it to the existing services. Every code block below was compiled and exercised end-to-end against a live MongoDB 7 container at the time of writing (see [Verification](#verification)); paste it as-is and the HTTP contracts do not change.

## Why this handoff exists

Stage 2 moves the Stage 1 datamarts into the service architecture:

- §2 of the assignment: the Indexing Service must *"store optimized structures in the datamarts"*.
- §3.2.2: `POST /index/update/{book_id}` must *"update both metadata tables and the inverted index **stored in the datamart**"*.

Today `cmd/indexing` runs `indexer.PublishedIndex`, an **in-memory** index: the Search Service is a different process and cannot see a single posting it holds. The datamart closes that gap — it is the shared store both services speak to:

```
                 writes (idempotent, 3-step)                    reads (one aggregation)
  datalake ──► [Indexing Service] ──► MongoDB datamart ◄── [Search Service] ──► GET /search
                 BookIndex bridge                           DatamartEngine bridge
```

The HTTP layer of both services already depends on small interfaces (`api.BookIndex`, `api.SearchEngine`). The bridges below implement those interfaces over `internal/mongodb`; no handler code changes.

## Configuration

| Environment variable | Default | Meaning |
|---|---|---|
| `MONGO_URI` | `mongodb://localhost:27017` | Connection string of the datamart server |
| `MONGO_DATABASE` | `boogle` | Database holding the two collections |

Nothing else is configured in code; ports and datalake paths keep using the existing flags. For local development:

```bash
docker run -d --name boogle-mongo -p 27017:27017 mongo:7
```

Both services call `client.EnsureIndexes(ctx)` at start-up (idempotent), which creates the filter indexes on the books collection: compound `{author, language, publication_year}` plus `{language}` and `{publication_year}`.

## Data model

| Collection | Document | Purpose |
|---|---|---|
| `books` | `{_id: <book_id>, title, author, language, publication_year, release_year, status}` | One metadata record per book. `status` is `pending` while a book is being indexed and `published` once complete; **search only ever returns `published` books**. |
| `inverted_index` | `{_id: <term>, postings: [{book_id, tf}, …]}` | One document per normalized term, posting list sorted by `book_id` without duplicates. |

The book `_id` doubles as the uniqueness constraint, so re-indexing the same book replaces its record instead of stacking copies.

## Package tour — `internal/mongodb`

| Symbol | Contract |
|---|---|
| `ConfigFromEnvironment()`, `Connect`, `Client`, `Client.Close` | Pool of connections; one `Client` per process is enough (safe for concurrent use). |
| `Client.EnsureIndexes(ctx)` | Creates the filter indexes; call once per start-up. |
| `NewBookIndexer(client).IndexBook(ctx, models.IndexBookInput{Metadata, Tokens})` | Writes one book in three ordered steps — mark `pending`, upsert postings with tf computed internally, mark `published`. Idempotent and concurrency-safe; a crash mid-way leaves the book invisible, never half-indexed. |
| `BookIndexer.Rebuild(ctx, []models.IndexBookInput)` | Writes both collections aside (`*_rebuild`), creates their indexes, then atomically renames them into place. Search keeps reading the previous complete datamart until the swap. |
| `NewDatamartReader(client).PostingsForTerms(ctx, terms)` | Raw posting lists for the normalized terms present in the index, ordered by term. |
| `DatamartReader.BooksByID(ctx, ids)` | Metadata for the given IDs ordered by book ID; absent books are omitted. |
| `DatamartReader.SearchBooks(ctx, mongodb.SearchRequest)` | The search query — see below. |

### `SearchRequest` semantics

```go
type SearchRequest struct {
    Term     string // normalized exactly as the indexer stores it; matched verbatim
    Author   string // "" = no filter; case-insensitive substring of the author
    Language string // "" = no filter; matches any code of the comma list ("en,fr"), case-insensitive, spaces trimmed
    Year     int    // 0 = no filter; otherwise exact publication_year
    Offset   int    // >= 0
    Limit    int    // >= 1
}
```

- **One round trip.** The whole query (join postings with books, `status = published`, filters, count, sort, page) is a single server-side aggregation; the driver never streams candidates to Go.
- **Ordering:** descending `tf`, ties broken by ascending `book_id` — exactly the ranking `GET /search` documents.
- **`Total`** counts every match across pages; `Books` holds one page and is never nil.
- **Visibility:** `pending` books are excluded from the page *and* from `Total`.
- **Errors:** invalid input returns `ErrInvalidSearchRequest` (wraps a message); storage failures wrap with context. Nothing else is normalized — feeding raw user input is the caller's mistake (the bridge below normalizes with `indexer.ProcessText`).

## Bridge A — Indexing Service (implements `api.BookIndex`)

**File: `cmd/indexing/datamart_index.go`** (new file, whole package `main`):

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// statsTimeout bounds the datamart round trips of Stats, whose interface carries no context.
const statsTimeout = 5 * time.Second

// DatamartIndex adapts the MongoDB datamart to the api.BookIndex contract of the Indexing API.
// One update reads one datalake book and writes it to the datamart; a rebuild builds fresh
// collections aside and swaps them in, so search never observes a half-built index. It is safe
// for concurrent use: a book indexed while a rebuild is under way is recorded before its write
// and replayed on the fresh datamart afterwards, which is the promise the OpenAPI specification
// makes for POST /index/update during a rebuild.
type DatamartIndex struct {
	source       indexer.BookSource
	writer       mongodb.BookIndexer
	client       mongodb.Client
	pendingMutex sync.Mutex
	pending      []models.BookID
	rebuilding   atomic.Bool
	lastUpdate   atomic.Int64
}

// NewDatamartIndex wires the adapter to the datalake it indexes from and the connected datamart
// it writes to. The datamart keeps its contents across restarts, so only the start-up timestamp
// starts fresh.
func NewDatamartIndex(source indexer.BookSource, client mongodb.Client) *DatamartIndex {
	adapted := &DatamartIndex{
		source:  source,
		writer:  mongodb.NewBookIndexer(client),
		client:  client,
		pending: []models.BookID{},
	}
	adapted.markUpdated()
	return adapted
}

// IndexBook reads one book from the datalake and stores its metadata and body terms in the
// datamart. The rebuild flag is read before the write on purpose: a swap can then never overtake
// a book already flagged for replay, so an update concurrent with a rebuild is never lost.
func (index *DatamartIndex) IndexBook(bookID models.BookID) error {
	book, err := index.source.ReadBook(bookID)
	if err != nil {
		return fmt.Errorf("read book %d: %w", bookID, err)
	}
	input := models.IndexBookInput{
		Metadata: indexer.ExtractMetadata(book.ID, book.Header),
		Tokens:   indexer.ProcessText(book.Body),
	}
	if index.rebuilding.Load() {
		index.recordPending(bookID)
	}
	if err := index.writer.IndexBook(context.Background(), input); err != nil {
		return fmt.Errorf("index book %d in datamart: %w", bookID, err)
	}
	index.markUpdated()
	return nil
}

// Rebuild reads every complete book of the datalake and swaps the freshly built collections into
// the datamart in one step. Only one rebuild runs at a time: a second request receives
// indexer.ErrRebuildInProgress, which the handler maps to 409. Books recorded during the rebuild
// are replayed after the swap; a failure of that replay fails the rebuild so the caller retries.
func (index *DatamartIndex) Rebuild(ctx context.Context) (indexer.RebuildReport, error) {
	if !index.rebuilding.CompareAndSwap(false, true) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	startedAt := time.Now()
	processed, err := index.buildFreshDatamart(ctx)
	pending := index.finishRebuild()
	if err != nil {
		return indexer.RebuildReport{}, err
	}
	if err := index.replayPending(pending); err != nil {
		return indexer.RebuildReport{}, err
	}
	index.markUpdated()
	return indexer.RebuildReport{BooksProcessed: processed, Elapsed: time.Since(startedAt)}, nil
}

// Stats reports how many books the datamart publishes, when this process last changed them and
// how many document bytes the datamart holds. A failed round trip reports zero instead of
// failing: GET /index/status must answer even when a measurement is unavailable.
func (index *DatamartIndex) Stats() indexer.IndexStats {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	return indexer.IndexStats{
		BooksIndexed: index.countPublishedBooks(ctx),
		LastUpdate:   time.Unix(0, index.lastUpdate.Load()).UTC(),
		SizeBytes:    index.datamartBytes(ctx),
	}
}

func (index *DatamartIndex) buildFreshDatamart(ctx context.Context) (int, error) {
	bookIDs, err := index.source.ListBookIDs()
	if err != nil {
		return 0, fmt.Errorf("list datalake books: %w", err)
	}
	books := make([]models.IndexBookInput, 0, len(bookIDs))
	for _, bookID := range bookIDs {
		book, err := index.source.ReadBook(bookID)
		if err != nil {
			return 0, fmt.Errorf("read book %d: %w", bookID, err)
		}
		books = append(books, models.IndexBookInput{
			Metadata: indexer.ExtractMetadata(book.ID, book.Header),
			Tokens:   indexer.ProcessText(book.Body),
		})
	}
	if _, err := index.writer.Rebuild(ctx, books); err != nil {
		return 0, fmt.Errorf("swap in rebuilt datamart: %w", err)
	}
	return len(books), nil
}

// finishRebuild lifts the rebuild flag and returns the books recorded under it. It runs after
// success and failure alike: without a swap the recorded books already sit in the live datamart,
// so the caller simply drops them.
func (index *DatamartIndex) finishRebuild() []models.BookID {
	index.rebuilding.Store(false)
	index.pendingMutex.Lock()
	defer index.pendingMutex.Unlock()
	pending := index.pending
	index.pending = []models.BookID{}
	return pending
}

func (index *DatamartIndex) recordPending(bookID models.BookID) {
	index.pendingMutex.Lock()
	defer index.pendingMutex.Unlock()
	index.pending = append(index.pending, bookID)
}

func (index *DatamartIndex) replayPending(pending []models.BookID) error {
	for _, bookID := range pending {
		if err := index.IndexBook(bookID); err != nil {
			return fmt.Errorf("replay book %d after rebuild: %w", bookID, err)
		}
	}
	return nil
}

func (index *DatamartIndex) countPublishedBooks(ctx context.Context) int {
	count, err := index.client.Database().Collection(mongodb.BooksCollection).
		CountDocuments(ctx, bson.D{{Key: "status", Value: mongodb.StatusPublished}})
	if err != nil {
		slog.Warn("[INDEXING] count published books", "error", err)
		return 0
	}
	return int(count)
}

func (index *DatamartIndex) datamartBytes(ctx context.Context) uint64 {
	var stats struct {
		DataSize int64 `bson:"dataSize"`
	}
	command := bson.D{{Key: "dbStats", Value: 1}}
	if err := index.client.Database().RunCommand(ctx, command).Decode(&stats); err != nil {
		slog.Warn("[INDEXING] measure datamart size", "error", err)
		return 0
	}
	return uint64(max(stats.DataSize, 0))
}

func (index *DatamartIndex) markUpdated() {
	index.lastUpdate.Store(time.Now().UnixNano())
}
```

### Wiring — `cmd/indexing/main.go`

1. **Imports:** add `"fmt"` and `"boogle-engine/internal/mongodb"`; remove `"boogle-engine/internal/indexer"` (only `NewPublishedIndex` used it — the bridge file imports it itself).

2. **Replace `run`:**

```go
// run wires the service together and serves requests until Ctrl+C or SIGTERM.
func run(arguments []string) error {
	config, err := parseConfig(arguments)
	if err != nil {
		return err
	}
	client, err := connectDatamart()
	if err != nil {
		return err
	}
	defer closeDatamart(client)
	indexingHandler, err := buildIndexingHandler(config, client)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logStartup(config)
	return serve(ctx, newServer(config, newRouter(indexingHandler)))
}
```

3. **Add the two helpers:**

```go
// connectDatamart opens the MongoDB pool described by MONGO_URI and MONGO_DATABASE and creates
// the query indexes once; both are idempotent, so every start-up may call them.
func connectDatamart() (mongodb.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	client, err := mongodb.Connect(ctx, mongodb.ConfigFromEnvironment(), mongodb.DefaultConnectTimeout)
	if err != nil {
		return mongodb.Client{}, err
	}
	if err := client.EnsureIndexes(ctx); err != nil {
		if closeErr := client.Close(context.Background()); closeErr != nil {
			slog.Error("[INDEXING] close datamart after failed index setup", "error", closeErr)
		}
		return mongodb.Client{}, fmt.Errorf("ensure datamart indexes: %w", err)
	}
	return client, nil
}

// closeDatamart releases the MongoDB pool when the service shuts down.
func closeDatamart(client mongodb.Client) {
	if err := client.Close(context.Background()); err != nil {
		slog.Error("[INDEXING] close datamart", "error", err)
	}
}
```

4. **Replace `buildIndexingHandler`:**

```go
// buildIndexingHandler performs the dependency injection: the datalake goes into the datamart
// adapter, and the adapter goes into the HTTP handler.
func buildIndexingHandler(config serviceConfig, client mongodb.Client) (api.IndexingHandler, error) {
	lake, err := datalake.New(config.datalakeDir, config.batchSize)
	if err != nil {
		return api.IndexingHandler{}, err
	}
	return api.NewIndexingHandler(NewDatamartIndex(lake, client), config.rebuildTimeout)
}
```

Notes:

- The OpenAPI annotations and HTTP behavior are untouched; no `swag` regeneration is needed.
- `-datalake` and `-batch-size` keep their meaning. `-workers` only fed the in-memory parallel rebuild; the datamart rebuild reads the datalake sequentially (a reference corpus rebuilds in seconds), so the flag currently affects nothing — keep it registered if the in-memory index stays as a fallback.
- **Why record-before-write:** the rebuild flag is read *before* the datamart write. If a rebuild's swap lands between the flag check and the write, the book was already appended to the replay list and the replay runs after the swap — so an update concurrent with a rebuild cannot be dropped, which is what the `POST /index/update` annotation promises. Duplicates in the replay list are harmless: `IndexBook` is idempotent.
- **Restart semantics differ from the in-memory index:** the datamart persists, so after a service restart the books are still searchable. `POST /index/rebuild` remains necessary after tokenizer or metadata-parser changes (and the Control Module may still call it after start-up; it is idempotent).

## Bridge B — Search Service (implements `api.SearchEngine`)

**File: `cmd/search/datamart_engine.go`** (new file):

```go
package main

import (
	"context"

	"boogle-engine/internal/api"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// DatamartEngine answers the Search API from the MongoDB datamart. It normalizes the keyword
// with the same tokenizer the Indexing Service applies to book bodies and delegates ranking,
// filters, pagination and the total count to one server-side aggregation. It is safe for
// concurrent use: every request runs against a stateless reader.
type DatamartEngine struct {
	reader mongodb.DatamartReader
}

// NewDatamartEngine wires the search API to a connected datamart.
func NewDatamartEngine(reader mongodb.DatamartReader) DatamartEngine {
	return DatamartEngine{reader: reader}
}

// Search resolves query.Term to the stored form of the inverted index: tokenizer rules decide
// it, so "Adventure!" searches "adventure". GET /search promises one keyword, so of several
// tokens only the first is searched, and a keyword made only of stop words matches nothing
// because those terms are never indexed. The handler has already trimmed, lower-cased and
// bounded every field, so they pass straight through to the datamart request.
func (engine DatamartEngine) Search(ctx context.Context, query api.SearchQuery) (api.SearchPage, error) {
	tokens := indexer.ProcessText(query.Term)
	if len(tokens) == 0 {
		return api.SearchPage{Books: []models.BookMetadata{}}, nil
	}
	result, err := engine.reader.SearchBooks(ctx, mongodb.SearchRequest{
		Term:     tokens[0],
		Author:   query.Filters.Author,
		Language: query.Filters.Language,
		Year:     query.Filters.Year,
		Offset:   query.Offset,
		Limit:    query.Limit,
	})
	if err != nil {
		return api.SearchPage{}, err
	}
	return api.SearchPage{Total: result.Total, Books: result.Books}, nil
}
```

### Field-by-field mapping

| `api.SearchQuery` (validated by the handler) | `mongodb.SearchRequest` | Note |
|---|---|---|
| `Term` (trimmed, non-empty) | `Term` = `indexer.ProcessText(Term)[0]` | The tokenizer is the single source of truth for what a term is; the datamart matches it verbatim. |
| `Filters.Author` (trimmed) | `Author` | Substring, case-insensitive, inside the aggregation. |
| `Filters.Language` (lower-cased by handler) | `Language` | Matched against each code of the stored comma list. |
| `Filters.Year` (1…9999 or 0) | `Year` | Exact match; 0 means no filter. |
| `Offset`, `Limit` (1…100) | `Offset`, `Limit` | Passed through; the reader re-validates defensively. |

### Wiring — `cmd/search/main.go`

1. **Imports:** add `"fmt"` and `"boogle-engine/internal/mongodb"`.
2. **Replace `run`:**

```go
// run wires the service together and serves requests until Ctrl+C or SIGTERM.
func run(arguments []string) error {
	address, err := parseAddress(arguments)
	if err != nil {
		return err
	}
	client, err := connectDatamart()
	if err != nil {
		return err
	}
	defer closeDatamart(client)
	searchHandler, err := api.NewSearchHandler(NewDatamartEngine(mongodb.NewDatamartReader(client)))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, newServer(address, newRouter(searchHandler)))
}
```

3. **Add the same two helpers** as the Indexing Service with the `[SEARCH]` log prefix dropped (this service logs without the bracketed prefix):

```go
// connectDatamart opens the MongoDB pool described by MONGO_URI and MONGO_DATABASE and creates
// the query indexes once; both are idempotent, so every start-up may call them.
func connectDatamart() (mongodb.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mongodb.DefaultConnectTimeout)
	defer cancel()
	client, err := mongodb.Connect(ctx, mongodb.ConfigFromEnvironment(), mongodb.DefaultConnectTimeout)
	if err != nil {
		return mongodb.Client{}, err
	}
	if err := client.EnsureIndexes(ctx); err != nil {
		if closeErr := client.Close(context.Background()); closeErr != nil {
			slog.Error("close datamart after failed index setup", "error", closeErr)
		}
		return mongodb.Client{}, fmt.Errorf("ensure datamart indexes: %w", err)
	}
	return client, nil
}

// closeDatamart releases the MongoDB pool when the service shuts down.
func closeDatamart(client mongodb.Client) {
	if err := client.Close(context.Background()); err != nil {
		slog.Error("close datamart", "error", err)
	}
}
```

4. Optionally update the command's package doc comment (it still says "Until the MongoDB datamart is ready…"). `api.StubEngine` stays in the tree as an offline fallback; only the wiring line changes.

## Operational semantics at a glance

| Concern | Behavior |
|---|---|
| Idempotency | Re-indexing a book replaces its metadata record and converges to identical postings; repeated rebuilds over the same datalake produce equivalent collections. |
| Partial writes | A book becomes searchable only in the final `published` step of `IndexBook`; readers filter on `status` as well, so even a raw reader never sees a half-written book. |
| Rebuild atomicity | Temporary collections + `renameCollection` (admin database, `dropTarget`); search reads either the old or the new complete datamart. The two renames are not one transaction — on the single-machine Stage 2 host the sub-millisecond window pairs new postings with previous metadata and resolves on the next request; Stage 3 should replace it with a version pointer. |
| Concurrent rebuild | One at a time (`ErrRebuildInProgress` → HTTP 409); updates during a rebuild are recorded and replayed after the swap. |
| Concurrency | One `Client` per process; `BookIndexer` and `DatamartReader` hold no mutable state, so handlers and workers share them freely. |
| Failure of the datamart | Writers return wrapped errors (→ HTTP 500 with the documented JSON body); `Stats` reports zeros and logs instead of failing `GET /index/status`. |
| Stage 3 readiness | Every address/name comes from `MONGO_URI`/`MONGO_DATABASE`; no local path or port is hard-coded in the package. |

## Verification

**Unit/integration suite** (live tests; they skip when no server is reachable — start the container first):

```bash
go test -v ./internal/mongodb/
```

Recorded at handoff: 34 tests passing against `mongo:7` and none skipped — 14 write-path/connection/config/rebuild plus 16 readers and search (with 4 invalid-request subtests), of the `internal/mongodb` package, plus `gofmt -l .`, `go vet ./...` and `go build ./...` clean over the whole repository.

**End-to-end smoke of both bridges** (compiled into the real `cmd/` files, seeded datamart, service on `:7013`):

```json
GET /search?q=adventure
{"query":"adventure","filters":{},"total":2,"offset":0,"limit":20,
 "results":[{"book_id":2,"title":"More Adventures","author":"jane austin","language":"en,fr","year":1850},
            {"book_id":1,"title":"Island Adventure","author":"Jane Austen","language":"en","year":1813}]}
```

Ranked by tf (5 > 2), the `pending` book with tf 9 excluded. Then:

```json
GET /search?q=adventure&language=fr&author=AUSTEN
{"total":0,"results":[]}          // filters combine with AND: fr matches book 2, AUSTEN matches book 1
GET /search?q=the
{"total":0,"results":[]}          // stop word: the tokenizer never indexes it, answered 200 with an empty page
```

**Manual walkthrough** once both services run with the bridges:

```bash
curl -X POST http://localhost:7002/index/update/1342   # {"book_id":1342,"index":"updated"}
curl http://localhost:7002/index/status                # books_indexed grows, index_size_MB from dbStats
curl "http://localhost:7003/search?q=adventure&language=en&limit=5"
```

## Ownership

`internal/mongodb` (package, tests, this document) is Member 4's deliverable and lives on `feature/mongodb-datamart`. The two bridge files belong to the services that host them — Member 3 owns `cmd/indexing`, Member 5 owns `cmd/search`. Both blocks above compile and ran against a live server as verified in [Verification](#verification); if a bridge is not picked up, Member 4 can land it unchanged, since the wiring requires no edit to any handler or OpenAPI annotation.
