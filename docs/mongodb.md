# MongoDB Datamart — Integration Guide

**Owner:** Member 4 (MongoDB & datamarts) · **Audience:** Member 3 (Indexing Service), Member 5 (Search Service), and anyone wiring the services together.

This document hands over `internal/mongodb` — the datamart storage of Boogle Engine — and the two small adapter files that connect it to the existing services. Every code block below was compiled and exercised end-to-end against a live MongoDB 7 container at the time of writing (see [Verification](#verification)); paste it as-is and the HTTP contracts do not change.

## Why this handoff exists

Stage 2 moves the Stage 1 datamarts into the service architecture:

- §2 of the assignment: the Indexing Service must *"store optimized structures in the datamarts"*.
- §3.2.2: `POST /index/update/{book_id}` must *"update both metadata tables and the inverted index **stored in the datamart**"*.

At handoff `cmd/indexing` ran `indexer.PublishedIndex`, an **in-memory** index: the Search Service is a different process and cannot see a single posting it holds. That type was removed after the review, once the datamart bridge replaced it. The datamart closes that gap — it is the shared store both services speak to:

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

**Server floor: MongoDB 5.2 or newer.** The write path's posting pipeline uses the `$sortArray` aggregation operator (introduced in 5.2). The package was developed and verified against MongoDB 7.0.45 — the version in the compose stack and behind every evidence run — and needs no newer server feature.

Both services call `client.EnsureIndexes(ctx)` at start-up (idempotent), which creates the filter indexes on the books collection — compound `{author, language, publication_year}` plus `{language}` and `{publication_year}` — and the multikey `postings.book_id` index that lets the write path drop a book's previous postings in one bounded update.

## Data model

| Collection | Document | Purpose |
|---|---|---|
| `books` | `{_id: <book_id>, title, author, language, publication_year, release_year, status}` | One metadata record per book. `status` is `pending` while a book is being indexed and `published` once complete; **search only ever returns `published` books**. |
| `inverted_index` | `{_id: <term>, postings: [{book_id, tf}, …]}` | One document per normalized term, posting list sorted by `book_id` without duplicates. |
| `locks` | `{_id: "rebuild", owner, expires_at}` | Single lease document that serializes rebuilds across every replica sharing the datamart. |

The book `_id` doubles as the uniqueness constraint, so re-indexing the same book replaces its record instead of stacking copies. A re-index also drops the book's postings from every term the new version no longer contains; the resulting empty term documents never match a search and the next rebuild discards them. `publication_year` is the original publication year, falling back to the Gutenberg release year when the header omits it, so the year filter still reaches those books.

## Package tour — `internal/mongodb`

| Symbol | Contract |
|---|---|
| `ConfigFromEnvironment()`, `Connect`, `Client`, `Client.Close` | Pool of connections; one `Client` per process is enough (safe for concurrent use). |
| `Client.EnsureIndexes(ctx)` | Creates the filter indexes; call once per start-up. |
| `NewBookIndexer(client).IndexBook(ctx, models.IndexBookInput{Metadata, Tokens})` | Writes one book in four ordered steps — mark `pending`, drop the previous version's postings, upsert the current postings with tf computed internally, mark `published`. Idempotent and concurrency-safe; a crash mid-way leaves the book invisible, never half-indexed. |
| `BookIndexer.Rebuild(ctx, mongodb.NextBook)` | Pulls one book at a time from the caller's stream, writes both collections aside (`*_rebuild`), creates their indexes, then atomically renames them into place. Search keeps reading the previous complete datamart until the swap, and memory holds one book, not the whole datalake. |
| `Client.AcquireRebuildLock(ctx)` | Claims the cluster-wide rebuild lease (`locks` collection); returns `ErrRebuildLocked` while another replica holds it, takes over an expired lease, renews it in the background and deletes it on `Release`. |
| `NewDatamartReader(client).PostingsForTerms(ctx, terms)` | Raw posting lists for the normalized terms present in the index, ordered by term. |
| `DatamartReader.BooksByID(ctx, ids)` | Metadata for the given IDs ordered by book ID; absent books are omitted. |
| `DatamartReader.SearchBooks(ctx, mongodb.SearchRequest)` | The search query — see below. |
| `DatamartReader.PublishedBook(ctx, bookID)`, `DatamartReader.FilterOptions(ctx)` (file `catalog_reader.go`) | The catalog endpoints: one published book's metadata and the distinct language/publication-year filter values, both computed from `status = published` books only, so an unknown or still-pending book stays invisible to readers exactly like in search results. |

### `SearchRequest` semantics

```go
type SearchRequest struct {
    Terms    []string // one or more terms normalized exactly as the indexer stores them; each matched verbatim, all required (AND)
    Author   string // "" = no filter; case-insensitive substring of the author
    Language string // "" = no filter; matches any code of the comma list ("en,fr"), case-insensitive, spaces trimmed
    Year     int    // 0 = no filter; otherwise exact publication_year (original year, or the release year when the header has no original date)
    Offset   int    // >= 0
    Limit    int    // >= 1
}
```

- **One round trip.** The whole query (join postings with books, `status = published`, filters, count, sort, page) is a single server-side aggregation; the driver never streams candidates to Go.
- **AND:** a book matches only when it appears in the posting list of every term; repeated terms count once. The postings are intersected (grouped per book) before the join with `books`, so extra terms shrink the join instead of growing it.
- **Ordering:** descending sum of the terms' `tf` in the book (plain `tf` for one term), ties broken by ascending `book_id` — exactly the ranking `GET /search` documents.
- **`Total`** counts every match across pages; `Books` holds one page and is never nil.
- **Visibility:** `pending` books are excluded from the page *and* from `Total`.
- **Errors:** invalid input returns `ErrInvalidSearchRequest` (wraps a message); storage failures wrap with context. Nothing else is normalized — feeding raw user input is the caller's mistake (the Search API handler splits `q` with `indexer.Tokenize` and `indexer.Normalize`).

## Bridge A — Indexing Service (implements `api.BookIndex`)

**File: `cmd/indexing/datamart_index.go`** (committed bridge, whole package `main`). The listing
below mirrors that file; when the bridge changes, update this listing in the same commit.

```go
package main

import (
	"context"
	"errors"
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
// One update reads one datalake book and writes it to the datamart; a rebuild streams the datalake
// book by book into fresh collections aside and swaps them in, so search never observes a
// half-built index. It is safe for concurrent use: a book indexed while a rebuild is under way is
// recorded before its write and replayed on the fresh datamart afterwards, which is the promise
// the OpenAPI specification makes for POST /index/update during a rebuild. Rebuilds are
// serialized across every replica sharing the datamart by a lease in MongoDB, and concurrent
// updates are bounded by maxConcurrentUpdates.
type DatamartIndex struct {
	source       indexer.BookSource
	writer       mongodb.BookIndexer
	client       mongodb.Client
	updateSlots  chan struct{}
	pendingMutex sync.Mutex
	pending      []models.BookID
	rebuilding   atomic.Bool
	lastUpdate   atomic.Int64
}

// NewDatamartIndex wires the adapter to the datalake it indexes from and the connected datamart
// it writes to. maxConcurrentUpdates caps how many POST /index/update requests the service runs
// at the same time. The datamart keeps its contents across restarts, so only the start-up
// timestamp starts fresh.
func NewDatamartIndex(source indexer.BookSource, client mongodb.Client, maxConcurrentUpdates int) (*DatamartIndex, error) {
	if maxConcurrentUpdates < 1 {
		return nil, fmt.Errorf("max concurrent updates must be a positive integer, got %d", maxConcurrentUpdates)
	}
	adapted := &DatamartIndex{
		source:      source,
		writer:      mongodb.NewBookIndexer(client),
		client:      client,
		updateSlots: make(chan struct{}, maxConcurrentUpdates),
		pending:     []models.BookID{},
	}
	adapted.markUpdated()
	return adapted, nil
}

// IndexBook reads one book from the datalake and stores its metadata and body terms in the
// datamart. The rebuild flag is read before the write on purpose: a swap can then never overtake
// a book already flagged for replay, so an update concurrent with a rebuild is never lost. The
// request context bounds both the wait for an update slot and the datamart writes, so a caller
// that disconnects releases its slot instead of leaving work behind.
func (index *DatamartIndex) IndexBook(ctx context.Context, bookID models.BookID) error {
	if err := index.acquireUpdateSlot(ctx); err != nil {
		return fmt.Errorf("book %d: %w", bookID, err)
	}
	defer index.releaseUpdateSlot()
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
	if err := index.writer.IndexBook(ctx, input); err != nil {
		return fmt.Errorf("index book %d in datamart: %w", bookID, err)
	}
	index.markUpdated()
	return nil
}

// acquireUpdateSlot blocks until one update slot is free or ctx ends, so a burst of updates
// queues instead of exhausting the datamart connection pool.
func (index *DatamartIndex) acquireUpdateSlot(ctx context.Context) error {
	select {
	case index.updateSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for an update slot: %w", ctx.Err())
	}
}

func (index *DatamartIndex) releaseUpdateSlot() {
	<-index.updateSlots
}

// Rebuild reads every complete book of the datalake and swaps the freshly built collections into
// the datamart in one step. Only one rebuild runs at a time across the replicas that share the
// datamart: the request first claims the rebuild lease, and a request that finds it taken
// receives indexer.ErrRebuildInProgress, which the handler maps to 409. Books recorded during the
// rebuild are replayed after the swap; a failure of that replay fails the rebuild so the caller
// retries. The lease is held until the replay finishes, so no other replica can swap a datamart
// while this one still has books to replay.
func (index *DatamartIndex) Rebuild(ctx context.Context) (indexer.RebuildReport, error) {
	lock, err := index.client.AcquireRebuildLock(ctx)
	if errors.Is(err, mongodb.ErrRebuildLocked) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	if err != nil {
		return indexer.RebuildReport{}, fmt.Errorf("claim rebuild lock: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			slog.Error("[INDEXING] release rebuild lock", "error", releaseErr)
		}
	}()
	if !index.rebuilding.CompareAndSwap(false, true) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	startedAt := time.Now()
	processed, err := index.buildFreshDatamart(ctx)
	pending := index.finishRebuild()
	if err != nil {
		return indexer.RebuildReport{}, err
	}
	if err := index.replayPending(ctx, pending); err != nil {
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
	result, err := index.writer.Rebuild(ctx, index.nextBook(bookIDs))
	if err != nil {
		return 0, fmt.Errorf("rebuild datamart: %w", err)
	}
	return result.BooksProcessed, nil
}

// nextBook reads and tokenizes one book per call, so a rebuild holds a single book in memory at a
// time however large the datalake grows.
func (index *DatamartIndex) nextBook(bookIDs []models.BookID) mongodb.NextBook {
	position := 0
	return func(ctx context.Context) (models.IndexBookInput, bool, error) {
		if err := ctx.Err(); err != nil {
			return models.IndexBookInput{}, false, err
		}
		if position >= len(bookIDs) {
			return models.IndexBookInput{}, false, nil
		}
		bookID := bookIDs[position]
		position++
		book, err := index.source.ReadBook(bookID)
		if err != nil {
			return models.IndexBookInput{}, false, fmt.Errorf("read book %d: %w", bookID, err)
		}
		return models.IndexBookInput{
			Metadata: indexer.ExtractMetadata(book.ID, book.Header),
			Tokens:   indexer.ProcessText(book.Body),
		}, true, nil
	}
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

func (index *DatamartIndex) replayPending(ctx context.Context, pending []models.BookID) error {
	for _, bookID := range pending {
		if err := index.IndexBook(ctx, bookID); err != nil {
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

The wiring is committed next to the bridge, and `main.go` is the source of truth. The datamart touches three places:

- **`run`** calls `connectDatamart()` before building any handler, so a datamart that cannot be reached fails the start-up instead of accepting updates it cannot store.
- **`connectDatamart`** opens the pool from `MONGO_URI` / `MONGO_DATABASE` and calls `Client.EnsureIndexes` once at start-up (idempotent); `closeDatamart` releases the pool on shutdown.
- **`buildIndexingHandler`** adapts the datalake with `NewDatamartIndex(lake, client, config.maxConcurrentUpdates)` — the constructor validates the cap and returns an error — and hands the adapter to `api.NewIndexingHandler(index, config.rebuildTimeout)`.

Flags: `-max-concurrent-updates` (default `8`) caps simultaneous `POST /index/update` requests; `-pprof` exposes `/debug/pprof/` on a separate port (empty disables it); `-rebuild-timeout` bounds both `POST /index/rebuild` and the HTTP write deadline. `-datalake` and `-batch-size` keep their Stage 1 meaning. The rebuild reads the datalake sequentially through `NextBook`, so the old `-workers` flag is gone.

Notes:

- The OpenAPI annotations and HTTP behavior are untouched; no `swag` regeneration is needed.
- **Why record-before-write:** the rebuild flag is read *before* the datamart write. If a rebuild's swap lands between the flag check and the write, the book was already appended to the replay list and the replay runs after the swap — so an update concurrent with a rebuild cannot be dropped, which is what the `POST /index/update` annotation promises. Duplicates in the replay list are harmless: `IndexBook` is idempotent.
- **Restart semantics:** the datamart persists, so after a service restart the books are still searchable. `POST /index/rebuild` remains necessary after tokenizer or metadata-parser changes (and the Control Module may still call it after start-up; it is idempotent).

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
| `Terms` (1–10 distinct terms the handler derived from `q`) | `Terms` | The handler splits `q` with the indexer's tokenizer, the single source of truth for what a term is; the datamart matches each verbatim and requires all of them. `Term` (raw `q`, trimmed) is only echoed in the response. |
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

4. Optionally update the command's package doc comment (it still says "Until the MongoDB datamart is ready…").

**Status (30 Oct 2026):** landed. `cmd/search/datamart_engine.go` is committed and `run` wires it, sharing one `DatamartReader` with the catalog endpoints. The engine depends on a one-method `bookSearcher` interface (satisfied by `mongodb.DatamartReader`) so `cmd/search/datamart_engine_test.go` can unit-test it without a server; `datamart_engine_integration_test.go` exercises it against a live datamart and skips when none is reachable. `api.StubEngine` now lives in `internal/api/stub_engine_test.go`, so it exists only for the handler tests and cannot be compiled into a service binary.

**Update (31 Oct 2026): multi-word `q`.** `GET /search` now treats `q` as the AND of its words. The handler splits `q` with the indexer's tokenizer into at most 10 distinct terms (stop words, single letters and Roman numerals dropped), answers 400 `invalid_parameter` when `q` has no word or more than 10 distinct terms, and answers 200 with an empty page without querying the datamart when every word is one the index never stores. The engine passes `Terms` straight to `SearchRequest.Terms`. The Bridge B code block above is the original single-keyword handoff (`ProcessText(Term)[0]`); `cmd/search/datamart_engine.go` holds the current code.

## Operational semantics at a glance

| Concern | Behavior |
|---|---|
| Idempotency | Re-indexing a book replaces its metadata record, drops its postings from terms the new version no longer contains, and converges to identical postings; repeated rebuilds over the same datalake produce equivalent collections. |
| Partial writes | A book becomes searchable only in the final `published` step of `IndexBook`; readers filter on `status` as well, so even a raw reader never sees a half-written book. |
| Rebuild atomicity | Temporary collections + `renameCollection` (admin database, `dropTarget`); search reads either the old or the new complete datamart. The two renames are not one transaction — on the single-machine Stage 2 host the sub-millisecond window pairs new postings with previous metadata and resolves on the next request; Stage 3 should replace it with a version pointer. |
| Rebuild memory | The rebuild streams the datalake one book at a time through `NextBook`; it never materializes every token stream, so its footprint does not grow with the datalake. |
| Concurrent rebuild | One at a time across replicas (`locks` lease, renewed every 30 s and expiring 2 min after a crash; `ErrRebuildInProgress` → HTTP 409); updates during a rebuild are recorded by the rebuilding process and replayed after the swap. A deployment with several replicas should route updates to the rebuild owner, or retry them after the 200, because the replay bookkeeping is per process. |
| Update concurrency | `POST /index/update` waits for one of `-max-concurrent-updates` slots (8 by default) and passes the request context to the datamart writes; a caller that disconnects cancels its own update and frees its slot. |
| Year filter | `publication_year` is the original publication year, or the Gutenberg release year when the header has no original date; both are 0 only when neither is readable, and search ignores books with year 0. |
| Concurrency | One `Client` per process; `BookIndexer` and `DatamartReader` hold no mutable state, so handlers and workers share them freely. |
| Failure of the datamart | Writers return wrapped errors (→ HTTP 500 with the documented JSON body); `Stats` reports zeros and logs instead of failing `GET /index/status`. |
| Stage 3 readiness | Every address/name comes from `MONGO_URI`/`MONGO_DATABASE`; no local path or port is hard-coded in the package. |

## Stage 3 layout proposal

Stage 2 stores the inverted index as one document per term with the whole posting list embedded (`postings: [{book_id, tf}, ...]`). The layout suits the reference corpus and stays untouched here; two of its properties break at cluster scale, so Stage 3 should choose a replacement deliberately instead of inheriting it.

- **Write amplification.** Indexing a book rewrites the posting array of each of its terms (a real book carries ≈6,600 distinct terms), so the cost of a book grows with the average posting-list length of its terms; the load benchmark already shows MongoDB near saturation at ≈6 books/s on one host ([evidence](evidence/README.md)).
- **The 16 MB ceiling.** A BSON document is capped at 16 MB, and a posting entry costs roughly 25–30 bytes once field and array overhead is counted, so a popular term meets the ceiling at a few hundred thousand books — a crawl that keeps growing will hit it.

Two candidate layouts:

| | One document per `(term, book)` | Bucketed posting lists |
|---|---|---|
| Write | Insert or replace one small document per term of the book; no array rewrite | Rewrite one bounded bucket (e.g. ≤ 8 MB) instead of the whole list |
| Read | Range scan on `{term, tf: -1, book_id: 1}` yields the ranked slice in index order; `total` is a count on `{term}` | Fan out over the term's buckets, merge the top-K, then count |
| Storage | Higher per-posting overhead (a document per pair) | Close to today's layout |
| Ceiling | None | Raised per bucket; the bucket count grows with the corpus |
| Sharding | Hashed `(term, book_id)` spreads the writes; per-term queries scatter-gather | Term-local buckets keep a term on one shard, but hot terms concentrate there |

**Recommendation:** one document per `(term, book)`. Updates stop rewriting arrays, the ranked query reads its order straight from the compound index, and the layout shards without the hot-term skew of a term-partitioned posting list. **Versioning:** the Stage 2 rebuild publishes the fresh index with an atomic collection rename, which a sharded cluster does not support; Stage 3 should write into `inverted_index_<version>` and publish the current version through a pointer document (`{_id: "current", version: ...}`) that readers resolve once per request, preserving the read-old/write-new window the rename gives today.

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
