# Architecture

This document describes the architecture of Boogle Engine Stage 2 at two levels:

- **System level**: the processes (Ingestion, Indexing and Search Services, Control Module), the stores they share (datalake, MongoDB datamart, control state) and how they talk to each other.
- **Code level**: the hexagonal (ports and adapters) layout of the Go module, in which each use case lives in a core package that declares the interfaces it needs, and every technology (HTTP, MongoDB, the filesystem, Project Gutenberg) sits in an adapter behind one of those interfaces.

## System Overview

```mermaid
graph TB
    subgraph "Front-End Boundary"
        Browser[Web Browser]
    end

    subgraph "Service Layer"
        Nginx[nginx<br/>:7003<br/>least_conn]
        Search[Search Service<br/>N replicas, stateless]
        Ingestion[Ingestion Service<br/>:7001<br/>1 replica]
        Indexing[Indexing Service<br/>:7002<br/>1 replica]
        Control[Control Module<br/>CLI tool<br/>on-demand]
    end

    subgraph "Storage Layer"
        Datalake[datalake/<br/>Filesystem<br/>Raw books]
        Datamart[(MongoDB<br/>:27017<br/>Indexed data)]
        ControlState[control/<br/>Filesystem<br/>Pipeline state]
    end

    subgraph "External"
        Gutenberg[Project Gutenberg<br/>HTTP]
    end

    Browser -->|GET /search<br/>GET /books/:id<br/>GET /search/filters| Nginx
    Nginx --> Search
    Search -->|Read| Datamart

    Control -->|POST /ingest/:id<br/>GET /ingest/list| Ingestion
    Control -->|POST /index/update/:id<br/>GET /index/status| Indexing
    Control -->|GET /books/:id<br/>-verify only| Search
    Control -->|Read/Write| ControlState

    Ingestion -->|HTTP GET| Gutenberg
    Ingestion -->|Write raw files| Datalake

    Indexing -->|Read raw files| Datalake
    Indexing -->|Write index| Datamart

    style Search fill:#e1f5ff
    style Ingestion fill:#fff4e1
    style Indexing fill:#ffe1f5
    style Control fill:#f0f0f0
```

## Code Structure: Hexagonal Layout

Every process is built from the same three kinds of package:

- **Core** packages hold the domain model and the use cases. They declare, as Go interfaces, both the operations they offer (*driving ports*) and the operations they need from the outside world (*driven ports*). They import no HTTP, MongoDB or filesystem code.
- **Adapters** connect a core to a technology. *Driving adapters* (the HTTP handlers, the command line) call a core through its driving ports; *driven adapters* (MongoDB, the datalake, the Gutenberg client, the HTTP clients of the Control Module, the control-state files) implement its driven ports.
- **Composition roots** (`cmd/*`) read the configuration, construct the adapters, inject them into the core constructors and serve. They contain no business rules.

### Packages by layer

| Layer | Package | Responsibility |
|---|---|---|
| Core: domain | `internal/models` | Domain types (`BookID`, `Book`, `StoredBook`, `BookMetadata`, `IndexBookInput`, `IngestionResult`, `FilterValues`) and the cross-boundary errors (`ErrBookNotFound`, `ErrBookUnavailable`, `ErrTransient`, `ErrPermanent`). No struct tags, no imports outside the standard library. |
| Core: domain | `internal/indexer` | Tokenizer (`Tokenize`, `Normalize`, `ProcessText`) and metadata parser (`ExtractMetadata`), shared by indexing and search so a query term is normalized exactly like an indexed term. |
| Core: use case | `internal/ingestion` | `ingestion.Service`: one shared download per book (single-flight), bounded download slots, a per-ingestion budget, retries of transient failures, and the already-stored check that answers `available` without downloading. |
| Core: use case | `internal/indexing` | `indexing.Service`: index one book, rebuild the whole index under a cross-replica lease with replay of the updates made during the rebuild, report index statistics. Owns `ErrRebuildInProgress`, `IndexStats`, `RebuildReport`. |
| Core: use case | `internal/search` | `search.Service`: query parsing into at most 10 distinct terms, paging and year rules, filter normalization, and the empty-terms short-circuit. `search.CatalogService`: one published book and the `GET /search/filters` values. |
| Core: use case | `internal/control` | Orchestrator (worker pool, book selector, retries with backoff), `Verifier` (`-verify`), `FailureRequeuer` (`-retry-failed`), `ReadinessCheck`, `Discoverer` (`-discover`). |
| Driving adapter | `internal/api` | Shared HTTP kit used by the three APIs: `ErrorResponse`, `WriteJSON`, `ParseBookIDPath`, `WriteInvalidBookID`, `InternalErrorCode`, `InvalidBookIDCode`, `BookNotFoundCode`. Test helpers live in `internal/api/apitest`. |
| Driving adapter | `internal/api/ingestionapi` | Ingestion API handlers (`POST /ingest/{book_id}`, `GET /ingest/status/{book_id}`, `GET /ingest/list`) and their response types. |
| Driving adapter | `internal/api/indexingapi` | Indexing API handlers (`POST /index/update/{book_id}`, `POST /index/rebuild`, `GET /index/status`), including the slot queue that bounds concurrent updates. |
| Driving adapter | `internal/api/searchapi` | Search API handlers (`GET /search`, `GET /books/{book_id}`, `GET /search/filters`) and the CORS policy; only this API is browser-facing. |
| Driving adapter | `cmd/control` (flags) | Turns command-line flags into calls to the control use cases. |
| Driven adapter | `internal/crawler` | `GutenbergClient` (implements `ingestion.BookFetcher`) and the header/body splitter. |
| Driven adapter | `internal/datalake` | Batch folder layout, atomic file writes, listing of complete books (implements `ingestion.BookStore` and `indexing.BookSource`). |
| Driven adapter | `internal/mongodb` | Datamart: connection, schema and indexes, write path, rebuild and rebuild lease, readers and the search aggregation (implements the driven ports of `indexing` and `search`). |
| Driven adapter | `internal/control/httpclient` | HTTP clients of the three service APIs (implement the service ports of `control`) and their wire types. |
| Driven adapter | `internal/localstate` | The `flock`-protected control-state files (implements the progress ports of `control`). |
| Composition root | `cmd/ingestion`, `cmd/indexing`, `cmd/search`, `cmd/control` | Configuration, wiring, HTTP server, graceful shutdown. |

### Ports and the adapters that implement them

```mermaid
graph LR
    subgraph Driving["Driving adapters"]
        IngAPI["api/ingestionapi"]
        IdxAPI["api/indexingapi"]
        SrcAPI["api/searchapi"]
        CLI["cmd/control<br/>flags"]
    end

    subgraph Core["Core"]
        Ing["ingestion.Service"]
        Idx["indexing.Service"]
        Src["search.Service<br/>search.CatalogService"]
        Ctl["control: Orchestrator, Verifier,<br/>FailureRequeuer, ReadinessCheck, Discoverer"]
    end

    subgraph Driven["Driven adapters"]
        Crawler["crawler.GutenbergClient"]
        Lake["datalake.Datalake"]
        Mongo["mongodb: BookIndexer, RebuildLocker,<br/>DatamartReader, SearchIndex"]
        HTTPC["control/httpclient:<br/>IngestionClient, IndexingClient, SearchClient"]
        Local["localstate.Store"]
    end

    IngAPI -->|BookIngester, DatalakeReader| Ing
    IdxAPI -->|BookIndex| Idx
    SrcAPI -->|Searcher, BookCatalog| Src
    CLI --> Ctl

    Ing -->|BookFetcher| Crawler
    Ing -->|BookStore| Lake
    Idx -->|BookSource| Lake
    Idx -->|IndexWriter, RebuildLocker, IndexMetrics| Mongo
    Src -->|Index, CatalogStore| Mongo
    Ctl -->|IngestionService, IndexingService,<br/>SearchService, ReadinessProbe, DatalakeBookLister| HTTPC
    Ctl -->|ProgressStore, IndexedBookLister, FailureResetter| Local

    style Ing fill:#fff4e1
    style Idx fill:#ffe1f5
    style Src fill:#e1f5ff
    style Ctl fill:#f0f0f0
```

Arrows show the direction of calls, labelled with the port used; every core also uses the shared domain packages `internal/models` and `internal/indexer`. Source-code dependencies all point **into** the core: a driving adapter imports the core to call it, and a driven adapter imports the core to implement its interfaces. No core package imports an adapter.

| Core | Driving ports (offered) | Driven ports (required) | Implemented by |
|---|---|---|---|
| `ingestion` | `BookIngester` (`Ingest`), `DatalakeReader` (`Locate`, `ListBookIDs`) | `BookFetcher` | `crawler.GutenbergClient` |
| | | `BookStore` | `datalake.Datalake` |
| `indexing` | `BookIndex` (`IndexBook`, `Rebuild`, `Stats`) | `BookSource` | `datalake.Datalake` |
| | | `IndexWriter` (with the `NextBook` stream) | `mongodb.BookIndexer` |
| | | `RebuildLocker` / `RebuildLease` | `mongodb.RebuildLocker` (lease in the `locks` collection) |
| | | `IndexMetrics` | `mongodb.DatamartReader` |
| `search` | `Searcher` (`Search`), `BookCatalog` (`PublishedBook`, `FilterOptions`) | `Index` | `mongodb.SearchIndex` |
| | | `CatalogStore` | `mongodb.DatamartReader` |
| `control` | none: `cmd/control` calls the use-case types directly | `IngestionService`, `ReadinessProbe`, `DatalakeBookLister` | `httpclient.IngestionClient` |
| | | `IndexingService`, `ReadinessProbe` | `httpclient.IndexingClient` |
| | | `SearchService` | `httpclient.SearchClient` |
| | | `ProgressStore`, `IndexedBookLister`, `FailureResetter` | `localstate.Store` |

### Rules the layout enforces

- **Ports belong to the core that consumes them.** Every interface outside test files is declared in a core package; the only exception is the unexported test seam `mongodb.booksSearcher`.
- **Adapters prove their ports at compile time.** Each driven adapter has a small `ports.go` with assertions such as `var _ search.Index = SearchIndex{}`, so a change to a port breaks the adapter's build instead of failing at run time.
- **Core constructors reject missing ports.** `indexing.NewService` and `ingestion.NewService` return `ErrMissingPort`, `search.NewService` and `search.NewCatalogService` return `ErrMissingIndex` and `ErrMissingCatalogStore`, and the control constructors return `ErrMissingDependency`, so a wiring mistake fails at start-up.
- **Wire and storage shapes live in adapters.** `internal/models` has no struct tags. The JSON response types are declared in the handler packages (`searchapi.SearchResponse`, `indexingapi.RebuildResponse`, ...), the stored BSON shape in `mongodb` (`bookDocument`), and the decoded answers of the internal APIs in `control/httpclient` (`IndexStatus`, `BookList`).
- **Errors cross boundaries as core errors.** Adapters translate technology errors into the errors of `internal/models` or of the core they serve. For example, `mongodb.RebuildLocker` wraps the adapter-level `ErrRebuildLocked` into `indexing.ErrRebuildInProgress`, which `indexingapi` maps to `409`; the Gutenberg client classifies HTTP failures as `ErrTransient` or `ErrBookUnavailable`, which drive the retries in `ingestion.Service`.
- **Each binary links only what it serves.** A service binary links `internal/api` and its own handler package; `cmd/control` links none, and no binary links `apitest` or `testing`.

### Wiring: the composition roots

The roots are the only place where concrete adapters meet the core. The Search Service, from `cmd/search/main.go` and `cmd/search/catalog.go`:

```go
reader := mongodb.NewDatamartReader(client)
searchHandler, err := buildSearchHandler(mongodb.NewSearchIndex(reader)) // search.NewService(index) → searchapi.NewSearchHandler
catalogHandler, err := buildCatalogHandler(reader)                       // search.NewCatalogService(store) → searchapi.NewCatalogHandler
```

The Indexing Service, from `cmd/indexing/main.go`:

```go
indexing.NewService(indexing.Ports{
    Source:  lake,                            // datalake.Datalake
    Writer:  mongodb.NewBookIndexer(client),
    Locker:  mongodb.NewRebuildLocker(client),
    Metrics: mongodb.NewDatamartReader(client),
})
```

The Ingestion Service builds `ingestion.NewService(ingestion.Ports{Fetcher: client, Store: lake}, retry, limits)` with a `crawler.GutenbergClient` and a `datalake.Datalake`, and hands the service to `ingestionapi.NewIngestionHandler` as both its ingester and its datalake reader. `cmd/control` loads `localstate.Store`, builds the three `httpclient` clients, and passes them to the control use cases.

### Testing by layer

- **Cores** are tested with in-memory fakes of their driven ports (`internal/*/service_test.go`, `internal/control/*_test.go`), so retry, single-flight, rebuild-replay and orchestration rules run without a network or a database.
- **Driving adapters** test the HTTP contract (status codes, error bodies, CORS), either against the real core with a fake driven port behind it (`searchapi/stub_index_test.go` implements `search.Index`) or against a fake driving port. Shared assertions live in `internal/api/apitest`.
- **Driven adapters** are tested against the real technology: `internal/mongodb` against a live MongoDB (the tests skip when none is reachable), `control/httpclient` against `httptest` servers, `localstate` and `datalake` against temporary directories.

## Data Flow: Ingestion to Search

```mermaid
sequenceDiagram
    participant User
    participant Control
    participant Ingestion
    participant Gutenberg
    participant Datalake
    participant Indexing
    participant MongoDB
    participant Search

    User->>Control: Run with -ids 1342,84
    Control->>Control: Load state from control/
    Control->>Ingestion: GET /ingest/list (readiness)
    Control->>Indexing: GET /index/status (readiness)

    Note over Control: For each book ID
    Control->>Ingestion: POST /ingest/1342
    Ingestion->>Gutenberg: HTTP GET pg1342.txt
    Gutenberg-->>Ingestion: Plain-text book
    Ingestion->>Datalake: Write batch_1000_1999/1342/<br/>1342_body.txt, then 1342_header.txt
    Ingestion-->>Control: 200 OK {status: "downloaded"}
    Control->>Control: Record in downloaded_books.txt

    Control->>Indexing: POST /index/update/1342
    Indexing->>Datalake: Read header and body
    Indexing->>Indexing: Extract metadata<br/>Tokenize body
    Indexing->>MongoDB: pending, drop old postings,<br/>upsert postings, published
    Indexing-->>Control: 200 OK {index: "updated"}
    Control->>Control: Record in indexed_books.txt

    Note over Control: Repeat for book 84

    User->>Search: GET /search?q=adventure
    Search->>Search: Parse q into terms (search core)
    Search->>MongoDB: One aggregation on inverted_index:<br/>AND match, score, filters, count, page
    MongoDB-->>Search: Total + one page of books
    Search-->>User: 200 OK {results: [...]}
```

## Concurrency Model: Control Module Workers

```mermaid
graph LR
    subgraph "Control Module Process"
        Selector[Book selector<br/>mutex-protected claim cursor]

        subgraph "Worker Pool (default: 4)"
            W1[Worker 1<br/>goroutine]
            W2[Worker 2<br/>goroutine]
            W3[Worker 3<br/>goroutine]
            W4[Worker 4<br/>goroutine]
        end
    end

    subgraph "External Services"
        Ingestion[Ingestion Service]
        Indexing[Indexing Service]
    end

    subgraph "Storage"
        State[control/<br/>localstate.Store<br/>flock-protected]
    end

    Selector --> W1
    Selector --> W2
    Selector --> W3
    Selector --> W4

    W1 -->|POST /ingest/:id| Ingestion
    W1 -->|POST /index/update/:id| Indexing
    W1 -->|Append| State

    W2 -->|POST /ingest/:id| Ingestion
    W2 -->|POST /index/update/:id| Indexing
    W2 -->|Append| State

    W3 -->|POST /ingest/:id| Ingestion
    W3 -->|POST /index/update/:id| Indexing
    W3 -->|Append| State

    W4 -->|POST /ingest/:id| Ingestion
    W4 -->|POST /index/update/:id| Indexing
    W4 -->|Append| State

    style W1 fill:#e1f5ff
    style W2 fill:#e1f5ff
    style W3 fill:#e1f5ff
    style W4 fill:#e1f5ff
```

## Storage Architecture

```mermaid
graph TB
    subgraph "Storage Layer (Single Host)"
        subgraph "Filesystem"
            subgraph Datalake["datalake/ (internal/datalake)"]
                direction TB
                B1[batch_1000_1999/1342/<br/>1342_header.txt<br/>1342_body.txt]
                B2[batch_0_999/84/<br/>84_header.txt<br/>84_body.txt]
                B3[.../<br/>...]
            end

            subgraph ControlState["control/ (internal/localstate)"]
                direction TB
                Downloaded[downloaded_books.txt<br/>append-only, flock]
                Indexed[indexed_books.txt<br/>append-only, flock]
                Failed[failed_books.txt<br/>append-only, flock]
            end
        end

        subgraph Datamart["MongoDB boogle database (internal/mongodb)"]
            direction TB
            Books[books collection<br/>_id: book_id<br/>Metadata + status]
            InvertedIndex[inverted_index collection<br/>_id: term<br/>Postings array]
            Locks[locks collection<br/>_id: rebuild<br/>Rebuild lease]
        end
    end

    Ingestion -->|Atomic write:<br/>temp + rename| B1
    Ingestion -->|Atomic write:<br/>temp + rename| B2

    Indexing -->|Read| B1
    Indexing -->|Read| B2
    Indexing -->|Upsert| Books
    Indexing -->|Upsert| InvertedIndex
    Indexing -->|Claim / renew| Locks

    Control -->|Append with flock| Downloaded
    Control -->|Append with flock| Indexed
    Control -->|Append with flock| Failed

    Search -->|Read| Books
    Search -->|Read| InvertedIndex

    style Datalake fill:#fff4e1
    style ControlState fill:#f0f0f0
    style Datamart fill:#ffe1f5
```

## Service Interaction and Thread Boundaries

```mermaid
graph TB
    subgraph "Host Machine (Single Machine)"
        subgraph "Ingestion Service Process"
            direction TB
            I_Server[HTTP Server<br/>Goroutine per request]
            I_Core[ingestion.Service<br/>single-flight per book<br/>8 download slots]
            I_FS[datalake.Datalake<br/>Atomic rename]
        end

        subgraph "Indexing Service Process"
            direction TB
            Idx_Server[HTTP Server<br/>Goroutine per request]
            Idx_Slots[indexingapi<br/>8 update slots]
            Idx_Core[indexing.Service<br/>rebuild flag + replay list]
        end

        subgraph "Search Service Process (per replica)"
            direction TB
            S_Server[HTTP Server<br/>Goroutine per request]
            S_Core[search.Service<br/>stateless]
        end

        subgraph "Control Module Process (On-Demand)"
            direction TB
            C_Selector[Book selector]
            C_Workers[Worker Pool<br/>N goroutines]
            C_State[localstate.Store<br/>RWMutex + flock]
        end

        subgraph "MongoDB Process"
            direction TB
            Mongo_Server[MongoDB Server]
            Mongo_Pool[Connection Pool<br/>maxPoolSize: 100 per client]
        end
    end

    C_Workers -->|HTTP POST| I_Server
    C_Workers -->|HTTP POST| Idx_Server
    I_Server --> I_Core
    I_Core -->|Store| I_FS
    Idx_Server --> Idx_Slots
    Idx_Slots --> Idx_Core
    Idx_Core -->|Read files| I_FS
    Idx_Core -->|Write index / rebuild| Mongo_Pool
    S_Server --> S_Core
    S_Core -->|One aggregation per query| Mongo_Pool

    style I_Server fill:#fff4e1
    style Idx_Server fill:#ffe1f5
    style S_Server fill:#e1f5ff
    style C_Workers fill:#f0f0f0
```

## Stage 3 Migration: What Changes

```mermaid
graph LR
    subgraph "Stage 2 (Current)"
        direction TB
        S2_Ingestion[Ingestion<br/>1 replica]
        S2_Indexing[Indexing<br/>1 replica]
        S2_Search[Search<br/>N replicas behind nginx]
        S2_Mongo[MongoDB<br/>1 instance]
        S2_FS[Shared Filesystem<br/>datalake/, control/]
    end

    subgraph "Stage 3 (Future)"
        direction TB
        S3_LB[Load Balancer<br/>nginx / HAProxy]
        S3_Ingestion[Ingestion<br/>2-4 replicas]
        S3_Indexing[Indexing<br/>replicas share the<br/>MongoDB rebuild lease]
        S3_Search[Search<br/>replicas on several nodes]
        S3_Mongo[(MongoDB<br/>Replica Set<br/>3 nodes)]
        S3_Object[Object Storage<br/>S3 / MinIO]
        S3_DB[(Database<br/>Control state)]
    end

    S2_Ingestion -.->|Scale horizontally| S3_Ingestion
    S2_Indexing -.->|Cluster-wide replay| S3_Indexing
    S2_Search -.->|Scale across nodes| S3_Search
    S2_Mongo -.->|Replica set| S3_Mongo
    S2_FS -.->|New datalake adapter| S3_Object
    S2_FS -.->|New control-state adapter| S3_DB

    S3_LB --> S3_Ingestion
    S3_LB --> S3_Search

    style S2_Ingestion fill:#fff4e1
    style S2_Indexing fill:#ffe1f5
    style S2_Search fill:#e1f5ff
    style S3_Ingestion fill:#fff4e1
    style S3_Indexing fill:#ffe1f5
    style S3_Search fill:#e1f5ff
```

The hexagonal layout turns most of these moves into adapter replacements. An object-storage datalake is a new adapter implementing `ingestion.BookStore` and `indexing.BookSource`; control state in a database is a new adapter implementing `control.ProgressStore`, `IndexedBookLister` and `FailureResetter`. The cores, the HTTP handlers and the API contracts stay as they are.

## Key Architectural Decisions

### 1. Classical HTTP Orchestration (No Message Broker)

**Decision:** Control Module coordinates services via direct HTTP calls.

**Rationale:**
- Simpler deployment (no Kafka/RabbitMQ infrastructure)
- Easier debugging (synchronous request-response)
- Adequate for single-host Stage 2

**Trade-off:**
- Control Module is a single point of failure
- Cannot scale control horizontally without distributed coordination

### 2. Filesystem-Based Datalake and Control State

**Decision:** Use local filesystem with atomic writes (temp file + rename) and flock.

**Rationale:**
- Simple, no external dependencies
- Fast for single-host deployment
- Crash-safe with atomic operations
- Both stores sit behind core ports (`internal/datalake`, `internal/localstate`), so replacing them does not touch the use cases

**Trade-off:**
- Cannot share across multiple hosts in Stage 3
- Must migrate to object storage (S3) and database

### 3. Rebuild Serialized by a Lease in MongoDB

**Decision:** `POST /index/rebuild` first claims a lease document in the `locks` collection (renewed every 30 s, expiring 2 minutes after a crash). Updates that arrive during a rebuild are recorded by the rebuilding process and replayed after the collections are swapped.

**Rationale:**
- The lease holds across every replica that shares the datamart, unlike an in-process mutex
- Updates are never blocked by a rebuild and never lost by its swap

**Trade-off:**
- The replay list is per process: with several Indexing replicas, updates must be routed to the lease holder or retried after the rebuild answers
- Stage 3 should move the replay bookkeeping into the datamart

### 4. MongoDB as Datamart

**Decision:** Use MongoDB for inverted index and metadata storage.

**Rationale:**
- Flexible schema for book metadata
- Good support for array fields (postings)
- Driver available for Go

**Trade-off:**
- Posting arrays may hit 16MB document limit at scale
- Full array rewrite on each update (expensive for large postings)
- Stage 3: consider sharding or alternative data model (one doc per term-book pair)

### 5. Worker Pool in Control Module

**Decision:** Configurable worker pool (default: 4) with goroutines.

**Rationale:**
- Concurrency without complexity of async programming
- Easy to tune via `-workers` flag

**Trade-off:**
- Optimal value depends on workload (benchmark shows 8 is best)
- Beyond optimal, contention reduces gains

### 6. Hexagonal Core with Explicit Ports

**Decision:** Each use case lives in a core package (`internal/ingestion`, `internal/indexing`, `internal/search`, `internal/control`) that declares its own driving and driven ports; HTTP, MongoDB, the filesystem and Gutenberg are adapters; `cmd/*` only wire them.

**Rationale:**
- Business rules (retries, single-flight, rebuild replay, query rules, orchestration) are tested with in-memory fakes, without a server
- A technology change for Stage 3 is a new adapter, not a rewrite of a service
- The API split (one handler package per service) keeps each binary limited to the code it serves

**Trade-off:**
- More packages and small interfaces to navigate
- Some rules are deliberately left in an adapter for performance (decision 7)

### 7. AND Matching and Relevance Ranking Run Inside the MongoDB Aggregation

**Decision:** The search core decides what a query *means* (terms, filters, page) and passes it through the `search.Index` port. Its evaluation is pushed down into one server-side aggregation in `internal/mongodb/search_reader.go`: the AND intersection of the posting lists, the relevance score (sum of the terms' `tf`, ties broken by ascending `book_id`), the published-only visibility, the filters, the total count and the requested page. This is a deliberate performance choice, not a gap in the layering.

**Rationale:**
- One round trip per query, and only one page of metadata crosses the network. Ranking in Go would require streaming every posting of every query term to the Search replica on each request, and the posting list of a common term covers most of the corpus.
- Postings are intersected before the `$lookup` into `books`, so each extra term shrinks the join instead of growing it.
- The total and the page come from the same `$facet`, so they are always consistent with each other.

**Trade-off:**
- The matching and ranking rules live in the MongoDB adapter, so a second `search.Index` adapter would have to re-implement them. The rules are documented as the port's contract in [mongodb.md](mongodb.md#searchrequest-semantics) and pinned by the live tests in `internal/mongodb/search_reader_test.go` (ranking by `tf`, tie-break by book ID, every term required, summed frequencies, pending books withheld, filters).

## Thread Safety Summary

| Component | Where | Mechanism | Scope | Stage 3 Impact |
|-----------|-------|-----------|-------|----------------|
| Control state files | `internal/localstate` | `sync.RWMutex` in memory; `flock` (Unix) / `LockFileEx` (Windows) on the files | Cross-process | Must move to database |
| Book claims | `internal/control` (book selector) | `sync.Mutex` on the claim cursor | Single process | Tasks collection with leases |
| Datalake writes | `internal/datalake` | Atomic rename (temp + `fsync` + final name) | Cross-process | Must move to object storage |
| Ingestion downloads | `internal/ingestion` | Single-flight map + `sync.Mutex`; buffered channel of 8 download slots | Single process | Scale with replicas |
| Index updates | `internal/api/indexingapi` | Buffered channel of 8 update slots | Single process | Scale with replicas |
| Rebuild replay | `internal/indexing` | `atomic.Bool` flag + `sync.Mutex` replay list | Single process | Move replay into the datamart |
| Rebuild exclusivity | `internal/mongodb` | Lease document in `locks`, claimed by a conditional update or an insert on its unique `_id` | Cross-replica | Already cluster-wide |
| MongoDB writes | `internal/mongodb` | Per-document atomic updates, idempotent upserts | Cross-process | Scale with replica set |
