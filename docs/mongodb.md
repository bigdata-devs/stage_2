# MongoDB Datamart — Integration Guide

**Owner:** Member 4 (MongoDB & datamarts) · **Audience:** Member 3 (Indexing Service), Member 5 (Search Service), and anyone wiring the services together.

This document describes `internal/mongodb`, the datamart storage of Boogle Engine. In the hexagonal layout of the module ([architecture.md](architecture.md#code-structure-hexagonal-layout)) it is a **driven adapter**: it implements the storage ports declared by the indexing and search cores, and it is the only package that imports the MongoDB driver. The document started as the handoff of the datamart to the service owners; it now describes the code as it stands after the hexagonal refactor, and the [History](#history) section records what changed.

## Why the datamart exists

Stage 2 moves the Stage 1 datamarts into the service architecture:

- §2 of the assignment: the Indexing Service must *"store optimized structures in the datamarts"*.
- §3.2.2: `POST /index/update/{book_id}` must *"update both metadata tables and the inverted index **stored in the datamart**"*.

At handoff `cmd/indexing` ran `indexer.PublishedIndex`, an **in-memory** index: the Search Service is a different process and cannot see a single posting it holds. That type was removed after the review, once the datamart replaced it. The datamart closes that gap — it is the shared store both services speak to:

```
                 writes (idempotent, 4-step)                    reads (one aggregation)
  datalake ──► [Indexing Service] ──► MongoDB datamart ◄── [Search Service] ──► GET /search
           indexing.Service → IndexWriter            search.Service → Index
```

Neither service talks to the driver directly. Each core declares the operations it needs as ports, and `internal/mongodb` implements them:

| Core | Port | Implemented by |
|---|---|---|
| `internal/indexing` | `IndexWriter` (write one book, rebuild from a `NextBook` stream) | `mongodb.BookIndexer` |
| `internal/indexing` | `RebuildLocker` / `RebuildLease` | `mongodb.RebuildLocker` |
| `internal/indexing` | `IndexMetrics` (published-book count, data size) | `mongodb.DatamartReader` |
| `internal/search` | `Index` (one ranked, filtered page) | `mongodb.SearchIndex` |
| `internal/search` | `CatalogStore` (one published book, raw filter values) | `mongodb.DatamartReader` |

The assertions in `indexing_ports.go` and `search_ports.go` make the build fail if an adapter stops satisfying its port. The HTTP handlers and the cores never import `internal/mongodb`; only the composition roots `cmd/indexing` and `cmd/search` do.

## Configuration

| Environment variable | Default | Meaning |
|---|---|---|
| `MONGO_URI` | `mongodb://localhost:27017` | Connection string of the datamart server |
| `MONGO_DATABASE` | `boogle` | Database holding the datamart collections |

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

| Symbol | Port | Contract |
|---|---|---|
| `ConfigFromEnvironment()`, `Connect`, `Client`, `Client.Close` | — | Pool of connections; one `Client` per process is enough (safe for concurrent use). |
| `Client.EnsureIndexes(ctx)` | — | Creates the filter indexes; call once per start-up. |
| `NewBookIndexer(client).IndexBook(ctx, models.IndexBookInput{Metadata, Tokens})` | `indexing.IndexWriter` | Writes one book in four ordered steps — mark `pending`, drop the previous version's postings, upsert the current postings with tf computed internally, mark `published`. Idempotent and concurrency-safe; a crash mid-way leaves the book invisible, never half-indexed. |
| `BookIndexer.Rebuild(ctx, indexing.NextBook)` | `indexing.IndexWriter` | Pulls one book at a time from the caller's stream, writes both collections aside (`*_rebuild`), creates their indexes, then atomically renames them into place and returns the number of books processed. Search keeps reading the previous complete datamart until the swap, and memory holds one book, not the whole datalake. |
| `Client.AcquireRebuildLock(ctx)` | — | Claims the cluster-wide rebuild lease (`locks` collection); returns `ErrRebuildLocked` while another replica holds it, takes over an expired lease, renews it in the background and deletes it on `Release`. |
| `NewRebuildLocker(client).AcquireRebuildLease(ctx)` | `indexing.RebuildLocker` | Wraps `AcquireRebuildLock` for the indexing core and translates `ErrRebuildLocked` into `indexing.ErrRebuildInProgress` (HTTP 409). |
| `NewDatamartReader(client).CountPublishedBooks(ctx)`, `DatamartReader.DataSizeBytes(ctx)` (file `index_metrics.go`) | `indexing.IndexMetrics` | Number of `published` books, and the `dataSize` reported by `dbStats`. Errors are returned; `indexing.Service.Stats` turns them into zeros and a log line so `GET /index/status` always answers. |
| `DatamartReader.SearchBooks(ctx, mongodb.SearchRequest)` | — | The search query — see below. |
| `NewSearchIndex(reader).Search(ctx, search.Query)` | `search.Index` | Maps the core's `search.Query` onto a `SearchRequest` and the result onto a `search.Page`. |
| `DatamartReader.PublishedBook(ctx, bookID)`, `DatamartReader.DistinctFilterValues(ctx)` (file `catalog_reader.go`) | `search.CatalogStore` | One published book's metadata, and the raw distinct `language` lists and `publication_year` values, both computed from `status = published` books only, so an unknown or still-pending book stays invisible to readers exactly like in search results. Turning the raw values into the `GET /search/filters` answer (language codes without the `Unknown` placeholder, years within the range `GET /search` accepts, de-duplicated and sorted) is a rule of `search.CatalogService.FilterOptions`, not of the adapter. |

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
- **Errors:** invalid input returns `ErrInvalidSearchRequest` (wraps a message); storage failures wrap with context. Nothing else is normalized — feeding raw user input is the caller's mistake (the search core, `internal/search`, splits `q` with `indexer.Tokenize` and `indexer.Normalize` before calling the `search.Index` port).

**Why evaluation lives in the aggregation (a deliberate push-down).** The search core decides what a query *means*: which terms, which filters, which page. Evaluating it — the AND intersection, the relevance score, the tie-break, the visibility rule, the filters, the total and the page — is left to this one aggregation on purpose, as a performance choice and not as a leak of business rules into the adapter. Run on the server, a query costs one round trip and returns one page of metadata; run in Go, it would have to stream every posting of every query term to the Search replica on each request, and the posting list of a common term spans most of the corpus. Intersecting before the `$lookup` also keeps the join proportional to the matches rather than to the postings. The price is that these rules are part of the `search.Index` contract: they are documented in this section and pinned by the live tests in `search_reader_test.go`, and any other `search.Index` adapter must reproduce them.

## Indexing Service — use case in `internal/indexing`, storage in `internal/mongodb`

The indexing use case is `indexing.Service` (`internal/indexing/service.go`). It owns everything that is not MongoDB-specific: reading a book through the `BookSource` port, extracting metadata and terms with `internal/indexer`, claiming the rebuild lease, recording the updates that arrive during a rebuild and replaying them after the swap, and reporting statistics. The datamart only stores what the service hands it.

### Wiring — `cmd/indexing/main.go`

The composition root is the source of truth. The datamart touches three places:

- **`run`** calls `connectDatamart()` before building any handler, so a datamart that cannot be reached fails the start-up instead of accepting updates it cannot store.
- **`connectDatamart`** opens the pool from `MONGO_URI` / `MONGO_DATABASE` and calls `Client.EnsureIndexes` once at start-up (idempotent); `closeDatamart` releases the pool on shutdown.
- **`buildIndexingHandler`** opens the datalake, builds the core with its four ports and hands it to `indexingapi.NewIndexingHandler(service, config.rebuildTimeout, config.maxConcurrentUpdates)`, whose constructor validates the cap and owns the slot queue that bounds `POST /index/update`:

```go
func newIndexingService(lake datalake.Datalake, client mongodb.Client) (*indexing.Service, error) {
	return indexing.NewService(indexing.Ports{
		Source:  lake,
		Writer:  mongodb.NewBookIndexer(client),
		Locker:  mongodb.NewRebuildLocker(client),
		Metrics: mongodb.NewDatamartReader(client),
	})
}
```

Flags: `-max-concurrent-updates` (default `8`) caps simultaneous `POST /index/update` requests; `-pprof` exposes `/debug/pprof/` on a separate port (empty disables it); `-rebuild-timeout` bounds both `POST /index/rebuild` and the HTTP write deadline. `-datalake` and `-batch-size` keep their Stage 1 meaning. The rebuild reads the datalake sequentially through `NextBook`, so there is no `-workers` flag.

Notes:

- **Why record-before-write:** `indexing.Service.IndexBook` reads the rebuild flag *before* the datamart write. If a rebuild's swap lands between the flag check and the write, the book was already appended to the replay list and the replay runs after the swap — so an update concurrent with a rebuild cannot be dropped, which is what the `POST /index/update` annotation promises. Duplicates in the replay list are harmless: `BookIndexer.IndexBook` is idempotent.
- **Restart semantics:** the datamart persists, so after a service restart the books are still searchable. `POST /index/rebuild` remains necessary after tokenizer or metadata-parser changes (and the Control Module may still call it after start-up; it is idempotent).
- **Tests:** `internal/indexing/service_test.go` exercises the rebuild lease, replay and statistics with in-memory fakes of the four ports; `cmd/indexing/indexing_service_test.go` runs the wired service against a live datamart and skips when none is reachable.

## Search Service — use case in `internal/search`, query in `internal/mongodb`

The search use case is `search.Service` (`internal/search`). It trims and validates the request, splits `q` into at most 10 distinct terms with the indexer's tokenizer, applies the paging and year rules, lower-cases the language filter, and answers an empty page without touching the datamart when every word of `q` is one the index never stores. Only then does it call the `search.Index` port, which `mongodb.SearchIndex` implements with one `SearchBooks` aggregation.

### Field-by-field mapping

| `search.Query` (built by the search core) | `mongodb.SearchRequest` | Note |
|---|---|---|
| `Terms` (1–10 distinct terms derived from `q`) | `Terms` | The core splits `q` with the indexer's tokenizer, the single source of truth for what a term is; the datamart matches each verbatim and requires all of them. `Text` (raw `q`, trimmed) is only echoed in the response. |
| `Filters.Author` (trimmed) | `Author` | Substring, case-insensitive, inside the aggregation. |
| `Filters.Language` (trimmed, lower-cased) | `Language` | Matched against each code of the stored comma list. |
| `Filters.Year` (1…9999 or 0) | `Year` | Exact match; 0 means no filter. |
| `Offset`, `Limit` (1…100) | `Offset`, `Limit` | Passed through; the reader re-validates defensively. |

### Wiring — `cmd/search/main.go`

`run` connects to the datamart with its own `connectDatamart` / `closeDatamart` pair, equivalent to the Indexing Service's (its log lines carry no bracketed prefix), then shares one `DatamartReader` between the search and catalog use cases:

```go
reader := mongodb.NewDatamartReader(client)
searchHandler, err := buildSearchHandler(mongodb.NewSearchIndex(reader))
if err != nil {
	return err
}
catalogHandler, err := buildCatalogHandler(reader)
if err != nil {
	return err
}
```

`buildSearchHandler` wraps the index in `search.NewService` and hands it to `searchapi.NewSearchHandler`; `buildCatalogHandler` (`cmd/search/catalog.go`) does the same with `search.NewCatalogService` and `searchapi.NewCatalogHandler`. The CORS policy of `searchapi` wraps the router.

Tests: `SearchIndex` depends on the unexported one-method `booksSearcher` interface (satisfied by `DatamartReader`), so `search_index_test.go` unit-tests the mapping without a server; `cmd/search/search_integration_test.go` exercises the wired service against a live datamart and skips when none is reachable. The handler tests use `StubIndex` from `internal/api/searchapi/stub_index_test.go`, an in-memory `search.Index` that exists only in test builds and cannot be compiled into a service binary.

## History

- **Handoff (October 2026).** The datamart reached the services through two bridge files in the composition roots: `cmd/indexing/datamart_index.go` (`DatamartIndex`, implementing `api.BookIndex`) and `cmd/search/datamart_engine.go` (`DatamartEngine`, implementing `api.SearchEngine`). The search stub lived in `internal/api/stub_engine_test.go`.
- **Multi-word `q` (31 Oct 2026).** `GET /search` treats `q` as the AND of its words: at most 10 distinct terms (stop words, single letters and Roman numerals dropped), `400 invalid_parameter` when `q` has no word or more than 10 distinct terms, and `200` with an empty page, without querying the datamart, when every word is one the index never stores. `SearchRequest.Term` became `SearchRequest.Terms`.
- **Hexagonal refactor (Nov 2026).** The bridges were dissolved. Their use-case logic moved into the cores (`indexing.Service`: rebuild lease, replay, statistics; `search.Service` and `search.CatalogService`: query rules and filter-value rule) and their MongoDB-specific parts into this package (`RebuildLocker`, `CountPublishedBooks`, `DataSizeBytes`, `SearchIndex`, `DistinctFilterValues`). The unused `PostingsForTerms` and `BooksByID` readers were removed; `BenchmarkPostingsForTerms` keeps measuring the posting fetch through a test-only helper. Stored documents and HTTP responses did not change.

## Operational semantics at a glance

| Concern | Behavior |
|---|---|
| Idempotency | Re-indexing a book replaces its metadata record, drops its postings from terms the new version no longer contains, and converges to identical postings; repeated rebuilds over the same datalake produce equivalent collections. |
| Partial writes | A book becomes searchable only in the final `published` step of `IndexBook`; readers filter on `status` as well, so even a raw reader never sees a half-written book. |
| Rebuild atomicity | Temporary collections + `renameCollection` (admin database, `dropTarget`); search reads either the old or the new complete datamart. The two renames are not one transaction — on the single-machine Stage 2 host the sub-millisecond window pairs new postings with previous metadata and resolves on the next request; Stage 3 should replace it with a version pointer. |
| Rebuild memory | The rebuild streams the datalake one book at a time through `NextBook`; it never materializes every token stream, so its footprint does not grow with the datalake. |
| Concurrent rebuild | One at a time across replicas (`locks` lease, renewed every 30 s and expiring 2 min after a crash; `ErrRebuildInProgress` → HTTP 409); updates during a rebuild are recorded by the rebuilding process and replayed after the swap. A deployment with several replicas should route updates to the rebuild owner, or retry them after the 200, because the replay bookkeeping is per process. |
| Update concurrency | The `POST /index/update` handler owns a queue of `-max-concurrent-updates` slots (8 by default): a request waits for a free slot and passes the request context down to the datamart writes, so a caller that disconnects while queued never reaches the index and one that disconnects mid-write cancels its own update. |
| Year filter | `publication_year` is the original publication year, or the Gutenberg release year when the header has no original date; both are 0 only when neither is readable, and search ignores books with year 0. |
| Concurrency | One `Client` per process; `BookIndexer`, `RebuildLocker`, `DatamartReader` and `SearchIndex` hold no mutable state, so handlers and workers share them freely. The only mutable indexing state (rebuild flag, replay list, last-update time) lives in `indexing.Service`. |
| Failure of the datamart | Writers return wrapped errors (→ HTTP 500 with the documented JSON body); `IndexMetrics` errors are turned into zeros and a log line by `indexing.Service.Stats` instead of failing `GET /index/status`. |
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

**End-to-end smoke** (recorded at handoff, with the bridges of that time compiled into the `cmd/` files, seeded datamart, service on `:7013`; the refactor kept the HTTP responses unchanged):

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

**Manual walkthrough** once both services run:

```bash
curl -X POST http://localhost:7002/index/update/1342   # {"book_id":1342,"index":"updated"}
curl http://localhost:7002/index/status                # books_indexed grows, index_size_MB from dbStats
curl "http://localhost:7003/search?q=adventure&language=en&limit=5"
```

## Ownership

`internal/mongodb` (package, tests, this document) is Member 4's deliverable; it was developed on `feature/mongodb-datamart`. The use cases that drive it belong to the cores of the services that host them — Member 3 owns `internal/indexing` and `cmd/indexing`, Member 5 owns `internal/search` and `cmd/search`. Because the cores reach the datamart only through their ports, a change inside `internal/mongodb` that keeps those ports and the stored document shapes needs no edit to any core, handler or OpenAPI annotation.
