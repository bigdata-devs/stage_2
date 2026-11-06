# API Inventory

Consolidated inventory of every HTTP endpoint the Boogle Engine exposes, as required by
spec §3.3. It covers the three HTTP services — Ingestion (`:7001`),
Indexing (`:7002`) and Search (`:7003`, fronted by nginx) — plus the operational
endpoints (health/status, Swagger UI, pprof) and the CORS policy of the front-facing
API. The Control Module is a CLI, not an HTTP service: it appears below only as a
consumer.

The machine-readable contracts live next to the code:
`api/ingestion/`, `api/indexing/` and `api/search/` each hold a generated
`swagger.json` / `swagger.yaml` / `docs.go` (regenerate with
`go generate ./cmd/<service>`), and every service serves its Swagger UI at
`GET /swagger/`. Error bodies always share the same envelope:
`{"error": "<code>", "message": "<human text>"}`.

## Audience taxonomy

| Audience | Meaning |
|---|---|
| **front-end** | Called by a browser page (through the nginx front door on `:7003`). |
| **internal** | Service-to-service call: the Control Module drives the pipeline over HTTP; never exposed to browsers. |
| **operational** | Health, status, administration, specification or profiling endpoint: used by operators, Docker healthchecks, benchmarks and scripts rather than by the pipeline itself. |

## Endpoint inventory

| Method | Route | Provider | Consumer | Audience | Purpose |
|---|---|---|---|---|---|
| `POST` | `/ingest/{book_id}` | Ingestion `:7001` | Control Module (`httpclient.Ingest`), validation/seed scripts | internal | Download one Gutenberg book and store its header + body in the datalake; `200` means fully written, idempotent. |
| `GET` | `/ingest/status/{book_id}` | Ingestion `:7001` | Control Module HTTP client (`Status`, available for readiness checks) | operational | Report whether a book is complete in the datalake (`available` or `404`). |
| `GET` | `/ingest/list` | Ingestion `:7001` | Control Module (`-discover`, readiness gate), Docker healthcheck, e2e/API scripts | internal, operational | List every complete book in the datalake with its count; readiness probe of the service. |
| `POST` | `/index/update/{book_id}` | Indexing `:7002` | Control Module (`httpclient`), validation/seed scripts, k6 benchmark | internal | Index one datalake book into the MongoDB datamart (metadata + inverted index); `200` means published. |
| `POST` | `/index/rebuild` | Indexing `:7002` | Operators (`curl`), benchmark protocol, evidence scripts | operational | Rebuild the whole index atomically from the datalake; cross-replica lease serializes concurrent rebuilds (`409`). |
| `GET` | `/index/status` | Indexing `:7002` | Control Module (readiness gate), Docker healthcheck, scripts | internal, operational | Index statistics: `books_indexed`, `last_update`, `index_size_MB`. |
| `GET` | `/search` | Search `:7003` | Front-end (via nginx), API validation, k6 benchmark, e2e scripts | front-end | Keyword search with AND semantics, author/language/year filters and pagination, ranked by term frequency. |
| `GET` | `/books/{book_id}` | Search `:7003` | Front-end (book detail view), Control Module (`-verify`) | front-end | Metadata of one published book (`404` if unknown or not yet published). |
| `GET` | `/search/filters` | Search `:7003` | Front-end (search form), Docker healthchecks (search + nginx) | front-end, operational | Distinct language codes and publication years of the published books; readiness probe of the service. |
| `GET` | `/swagger/` | All three services | Developers | operational | Swagger UI; spec served from `/swagger/doc.json`. |
| `GET` | `/debug/pprof/*` | Indexing (separate `-pprof` port, disabled by default) | Developers | operational | Go runtime profiling endpoints (`net/http/pprof`). |

All routes are registered with Go 1.22 method patterns (`"GET /path"`), so a request
with the wrong method on a known path answers `405 Method Not Allowed` instead of
reaching the handler.

## Ingestion Service (`:7001`)

Internal API that the Control Module consumes; not reachable from the front-end.

### `POST /ingest/{book_id}` — audience: internal

- **Path parameter:** `book_id` — positive integer (Project Gutenberg ID).
- **Body:** none.
- **Responses:**

| Status | `error` code | Meaning |
|---|---|---|
| `200` | — | Book complete in the datalake. `status` is `downloaded` (this call wrote it) or `available` (already stored); `path` holds the folder. |
| `400` | `invalid_book_id` | `book_id` is not a positive integer. |
| `404` | `book_unavailable` | Gutenberg has no such book or its text lacks the START/END markers. Permanent: do not retry. |
| `500` | `internal_error` | The datalake could not be written. |
| `503` | `download_failed` | Transient download failure or the ingestion budget (`-ingest-timeout`, 2 min) ran out: retry with backoff. |

- **Semantics:** synchronous (`200` = files fully written, atomic temp+rename), idempotent,
  concurrent calls for the same book are coalesced into one download.
- **Caller behaviour:** allow a timeout above the service budget of 2 min (the Control Module
  uses `-ingest-timeout 150s`); retry only `503 download_failed` with exponential backoff and
  never a `404 book_unavailable`.
- **Example:**

```console
$ curl -X POST http://localhost:7001/ingest/1342
{"book_id":1342,"status":"available","path":"/data/datalake/batch_1000_1999/1342"}
```

### `GET /ingest/status/{book_id}` — audience: operational

- **Responses:** `200` `{book_id, status:"available"}` when both files exist ·
  `400 invalid_book_id` · `404 book_not_found`.
- Read-only; safe to poll. Because writes are atomic, an ingestion still in progress
  answers `404` until its `POST /ingest/{book_id}` completes.
- The current pipeline does not need to poll it: `POST /ingest/{book_id}` is synchronous,
  so the Control Module indexes a book as soon as that call returns `200`. The client
  method exists for readiness checks and external monitoring.

### `GET /ingest/list` — audience: internal, operational

- **Responses:** `200` `{count, books:[…]}` (ascending IDs, never `null`) ·
  `500 internal_error` if the datalake folders cannot be read.
- Consumed by the Control Module readiness gate and `-discover`, and used as the
  Docker healthcheck of the `ingestion` container.

## Indexing Service (`:7002`)

Internal API that the Control Module consumes; not reachable from the front-end.

### `POST /index/update/{book_id}` — audience: internal

- **Path parameter:** `book_id` — positive integer. **Body:** none.
- **Responses:**

| Status | `error` code | Meaning |
|---|---|---|
| `200` | — | `{book_id, index:"updated"}`: metadata and postings are in the published index. |
| `400` | `invalid_book_id` | `book_id` is not a positive integer. |
| `404` | `book_not_found` | The datalake holds no complete copy: ingest first, then index. |
| `500` | `internal_error` | The book could not be read or indexed, or the caller stopped waiting for a free update slot. |

- **Semantics:** synchronous, idempotent (re-indexing replaces the book's metadata and
  postings); at most `-max-concurrent-updates` (default 8) books index at once, the rest
  queue; updates that arrive during a rebuild are replayed after the swap.
- **Caller behaviour:** a timeout of at least 30 s for a typical book (the Control Module
  uses `-timeout 30s`; a real novel can take longer under load); retry `500` after a
  transient outage, never `404 book_not_found` without ingesting the book first.
- **Example:**

```console
$ curl -X POST http://localhost:7002/index/update/1342
{"book_id":1342,"index":"updated"}
```

### `POST /index/rebuild` — audience: operational

- **Body:** none. **Responses:** `200` `{books_processed, elapsed_time}` ·
  `409 rebuild_in_progress` (another replica holds the lease) ·
  `503 rebuild_interrupted` (budget or disconnect; previous index still published) ·
  `500 internal_error`.
- Builds fresh collections and swaps them in atomically (`renameCollection`); safe to
  repeat, bounded by `-rebuild-timeout` (10 min default).

### `GET /index/status` — audience: internal, operational

- **Responses:** `200` `{books_indexed, last_update, index_size_MB}`.
- Used by the Control Module readiness gate, as the Docker healthcheck of the
  `indexing` container, and by scripts to wait until a seed or rebuild has finished.

## Search Service (`:7003`)

Front-facing API. The host port `7003` (`SEARCH_PORT`) is served by **nginx**, which
load-balances (`least_conn`) across the stateless Search replicas with a 5 s DNS
re-resolution; the requests below therefore reach a Search container through the proxy.

### `GET /search` — audience: front-end

- **Query parameters:**

| Param | Required | Rule |
|---|---|---|
| `q` | yes | Keywords; AND semantics over all words (max 10 distinct words). |
| `author` | no | Case-insensitive partial match. |
| `language` | no | ISO 639 code, case-insensitive exact match. |
| `year` | no | Exact publication year, 1–9999. |
| `offset` | no | ≥ 0, default `0`. |
| `limit` | no | 1–100, default `20`. |

- **Responses:** `200` `{query, filters, total, offset, limit, results:[…]}` (ranked by
  summed term frequency, ties by ascending `book_id`; `results` is `[]`, never `null`) ·
  `400 missing_parameter` (`q` absent) · `400 invalid_parameter` (no word in `q`, more
  than 10 distinct words, or malformed `year`/`offset`/`limit`) · `500 internal_error`.
  A query made only of stop words returns `200` with an empty page.
- **Example:**

```console
$ curl "http://localhost:7003/search?q=adventure&limit=1"
{"query":"adventure","filters":{},"total":160,"offset":0,"limit":1,"results":[{"book_id":169,"title":"The Well at the World's End: A Tale","author":"William Morris","language":"en","year":1994}]}
```

### `GET /books/{book_id}` — audience: front-end

- **Responses:** `200` book metadata · `400 invalid_book_id` ·
  `404 book_not_found` (unknown or not yet published) · `500 internal_error`.
- Read-only; also consumed by the Control Module `-verify` mode.

### `GET /search/filters` — audience: front-end, operational

- **Responses:** `200` `{languages:[…], years:[…]}` (sorted, empty arrays when nothing
  is published) · `500 internal_error`.
- Populates the front-end filter form and serves as Docker healthcheck for both the
  `search` containers and `nginx`.

## Operational endpoints

| Endpoint | Where | Purpose |
|---|---|---|
| `GET /ingest/list` | Ingestion `:7001` | Service readiness (Compose healthcheck: `wget http://127.0.0.1:7001/ingest/list`). |
| `GET /index/status` | Indexing `:7002` | Service readiness (Compose healthcheck: `wget http://127.0.0.1:7002/index/status`). |
| `GET /search/filters` | Search `:7003` | Service readiness (Compose healthchecks of `search` and `nginx`). |
| `GET /swagger/` + `/swagger/doc.json` | All three services | Swagger UI and generated OpenAPI document (source specs in `api/<service>/`). |
| `GET /debug/pprof/*` | Indexing, separate port (`-pprof`, e.g. `:7012`; empty disables it) | CPU/heap/goroutine profiles; deliberately bound to its own listener so profiling never shares the API's timeouts. |
| `POST /index/rebuild` | Indexing `:7002` | Administrative rebuild of the whole index (see above). |
| `mongosh ping` | MongoDB `:27017` | Compose healthcheck of the datamart (`db.adminCommand('ping').ok`). |

## CORS statement (Search Service, spec §3.3)

Only the Search Service answers browser cross-origin requests; the internal Ingestion
and Indexing APIs are never exposed to pages.

- **Configuration:** environment variable `CORS_ALLOWED_ORIGIN` — `*` (default; unset or
  blank also falls back to `*`) or a comma-separated list of origins such as
  `https://boogle.example.com,http://localhost:5173`. An unparseable value makes the
  service **fail at start-up** instead of silently blocking the front-end.
- **Allowed methods/headers:** `GET, HEAD, OPTIONS` and `Content-Type`.
- **Preflight:** `OPTIONS` carrying `Origin` + `Access-Control-Request-Method` is
  answered by the middleware with `204` (plus `Access-Control-Max-Age: 600`); from an
  origin outside the list it gets `403 origin_not_allowed`.
- **Simple responses:** every answer to an allowed origin — errors included — carries
  `Access-Control-Allow-Origin` (the request's own origin when an explicit list is
  configured, together with `Vary: Origin`). Requests from other origins are served
  without CORS headers, so the browser withholds the body.
- **Credentials:** never allowed; the policy never sends
  `Access-Control-Allow-Credentials`. The API is public and read-only.

## Notes

- **No non-HTTP interactions.** The coordination mechanism is classical orchestration over
  the HTTP endpoints above: there is no message broker, no scheduled coordination and no
  shared-memory region, so no event, schedule or shared-state contract applies. The only
  state outside HTTP is the Control Module's pipeline progress in `control/`
  files — append-only records under `flock`, owned and written only by the Control Module,
  whose layout and recovery rules are specified in
  [`docs/control.md`](control.md); this agrees with the diagrams in
  [`docs/architecture.md`](architecture.md).
- The **Control Module** (`cmd/control`) is an on-demand CLI, not an HTTP provider: it
  consumes `POST /ingest/{book_id}`, `GET /ingest/list`, `POST /index/update/{book_id}`,
  `GET /index/status` (and `GET /books/{book_id}` in `-verify` mode) and persists its
  own pipeline state in `control/` files.
- **Ports:** Ingestion `7001`, Indexing `7002`, Search replicas `7003` (container-side),
  nginx front door `7003` on the host (`SEARCH_PORT`), MongoDB `27017`; all overridable
  through `.env` (see `.env.example`).
- The **e2e and validation scripts** (`scripts/e2e_control.sh`, `scripts/e2e_compose.sh`,
  `scripts/api_validation.sh`) and the load benchmarks under `test/load/` exercise these
  same endpoints as external consumers; their recorded outputs live in
  `docs/evidence/`.
