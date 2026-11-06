# Control Module Runbook

The Control Module is the classical orchestrator of Boogle Engine. It drives the
pipeline book by book over HTTP — ingestion first, indexing second — and records
every completed step on disk so a restarted run resumes instead of duplicating work.

Its rules live in the control core (`internal/control`); HTTP and the state files
are adapters behind ports the core declares (see [Code layout](#code-layout)).

## Build and run

```bash
go build -o bin/control ./cmd/control
./bin/control -ids 1342,84,11 -workers 4
./bin/control -from 1 -to 500
./bin/control -discover
./bin/control -ids 1342,84 -retry-failed
./bin/control -verify -search-url http://localhost:7003
```

The module refuses to start when a required service is unreachable: it reads
`GET /ingest/list` on the Ingestion Service and `GET /index/status` on the
Indexing Service before the first book is claimed. Both checks share one
10 second readiness deadline (`control.ReadinessCheck`).

## Configuration

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-ingestion-url` | `INGESTION_URL` | `http://localhost:7001` | Base URL of the Ingestion Service |
| `-indexing-url` | `INDEXING_URL` | `http://localhost:7002` | Base URL of the Indexing Service |
| `-search-url` | `SEARCH_URL` | `http://localhost:7003` | Base URL of the Search Service (only used by `-verify`) |
| `-state-dir` | `CONTROL_STATE_DIR` | `control` | Folder of the control state files |
| `-ingest-timeout` | — | `150s` | Time limit of one `POST /ingest` call, above the service download budget (2 minutes by default) |
| `-timeout` | — | `30s` | Time limit of one indexing, status or readiness call |
| `-workers` | — | `4` | Concurrent pipeline workers |
| `-attempts` | — | `3` | Attempts per step on transient errors |
| `-backoff` | — | `1s` | Wait before the first retry (doubles each time) |
| `-retry-failed` | — | `false` | Re-enqueue every book recorded in `failed_books.txt` so the run tries them again |
| `-verify` | — | `false` | Reconcile `indexed_books.txt` with the datamart the Search Service exposes |
| `-ids` | — | — | Comma-separated book IDs |
| `-from`, `-to` | — | — | Inclusive range of book IDs |
| `-discover` | — | `false` | Add every complete book of the datalake reported by `GET /ingest/list` |

Book selection requires `-ids` and/or `-from`/`-to`, or the `-discover` flag (except in `-verify` mode, which skips orchestration entirely).
Flags override environment variables; all three endpoints are configurable so no
`localhost` address is hard-coded.

## Run the whole stack with Docker Compose

`docker-compose.yml` deploys MongoDB, the three services and (on demand) the
Control Module on any machine with Docker:

```bash
docker compose up -d --build                                     # mongo + ingestion + indexing + search + nginx
docker compose ps                                                # health of every container
docker compose --profile tools run --rm control -discover        # orchestrate the whole datalake
docker compose --profile tools run --rm control -ids 1342,84     # or an explicit selection
docker compose logs -f ingestion indexing search nginx           # follow the service logs
docker compose down                                              # stop; the named volumes are kept
```

The services reach each other by container name (`http://ingestion:7001`,
`http://indexing:7002`) through the environment variables of the compose file —
the same configurability that keeps `localhost` out of the code. Host ports come
from `.env` (copy `.env.example`); the Swagger UIs and the internal status routes
stay reachable on `localhost:7001..7003`, where `7003` is published by nginx in
front of the Search Service replicas (next section).

Containers carry code only: the datalake, the control state files and the
MongoDB datamart live in the named volumes `datalake`, `control-state` and
`mongo-data`. Recreating or killing a container therefore never loses pipeline
state, and a restarted Control Module resumes from `/data/control` exactly as
described in the failure-semantics section below.

### Search replicas behind nginx (audit RF 8)

The Search Service is stateless, so it runs as N replicas without a host port and
`nginx` (`deploy/nginx/nginx.conf`) balances `localhost:7003` across them with
`least_conn`. nginx re-resolves the `search` service name every 5 s, so scaling
needs no proxy restart; a replica that stops is taken out of rotation after a
refused connection (the request is retried on another replica) and rejoins 10 s
after it answers again. Every response names its replica in the `X-Upstream`
header, and every nginx log line carries `upstream=`.

```bash
docker compose up -d --build --scale search=2                    # or SEARCH_REPLICAS=2 in .env
docker compose -f docker-compose.yml -f docker-compose.replicas.yml up -d --build   # same, as an override file
docker compose ps search nginx                                    # search-1, search-2, nginx-1
docker compose logs nginx --no-log-prefix | grep -o 'upstream=[^ ]*' | sort | uniq -c   # load split
```

The replica experiment runs the same k6 profile twice against the same seeded
datamart, once with `--scale search=1` and once with `--scale search=2`, while
`test/load/sample_containers.sh` samples every search replica, nginx and MongoDB.

### Resource limits (for benchmarking)

`docker-compose.yml` carries commented resource limits for MongoDB, ingestion and
indexing; uncomment the `deploy.resources.limits` block of the service you want to
constrain. The search service already has a `deploy` key (its replica count), so
add `resources` under that key instead of a second `deploy`:

```yaml
services:
  ingestion:
    # ... other config ...
    deploy:
      resources:
        limits:
          cpus: '1.0'
          memory: 512M
  
  indexing:
    # ... other config ...
    deploy:
      resources:
        limits:
          cpus: '2.0'
          memory: 1G
  
  search:
    # ... other config ...
    deploy:
      replicas: ${SEARCH_REPLICAS:-1}
      resources:
        limits:
          cpus: '1.0'
          memory: 512M
  
  mongo:
    # ... other config ...
    deploy:
      resources:
        limits:
          cpus: '2.0'
          memory: 2G
```

These limits help simulate production constraints and measure performance under
resource constraints. Adjust the values based on your benchmarking needs.

## Orchestration flow

For every candidate book, one worker:

1. Skips the book when it is already indexed, or already recorded as failed.
2. `POST /ingest/{book_id}` and waits for the final answer. A `2xx` status means
   ingestion **has completed**; only then is the book recorded as downloaded.
3. `POST /index/update/{book_id}` and waits for the answer. A `2xx` status means the
   index update **has been published**; only then is the book recorded as indexed.

The Control Module is the only writer of the state files. Services must stay
idempotent: replayed `POST /ingest` calls answer `available`, replayed
`POST /index/update` calls never create duplicate postings.

## Control state files

| File | Format | Meaning |
|---|---|---|
| `control/downloaded_books.txt` | one book ID per line | Stored in the datalake |
| `control/indexed_books.txt` | one book ID per line | Fully indexed and published |
| `control/failed_books.txt` | `<book_id>\t<reason>` | Left the pipeline; not retried |

The files belong to the `internal/localstate` adapter (`localstate.Store`), which
implements the progress ports of the control core. Every append happens under an
exclusive `flock` (`LockFileEx` on Windows), so a crash or a second process can
never interleave partial lines. On load, malformed lines are skipped: replaying an
idempotent service call is safe, losing a book ID would not be. `localstate.Load`
logs `[CONTROL] control state loaded` with the downloaded, indexed and failed
counts, in every mode including `-verify` and `-retry-failed`. The on-disk format
is the same as before the hexagonal refactor, so existing state folders keep
working.

## Failure semantics

* **Transient failures** (network error, timeout, HTTP `408`/`429`/`5xx`, `failed`
  status answers) are retried with a doubling backoff up to `-attempts` times; a
  book that still fails after the last attempt is recorded in `failed_books.txt`.
* **Permanent failures** (any other HTTP `4xx`, `unavailable` status answers) abort
  retries immediately and are recorded in `failed_books.txt`. To retry such a book
  later, run with `-retry-failed` or delete its line from the file.

The `httpclient` adapters perform this classification: they map every HTTP answer
and transport error onto the core errors `models.ErrTransient` and
`models.ErrPermanent` (or `models.ErrBookUnavailable`), and the orchestrator
decides from those errors alone, without seeing a status code.
* **Interrupted books** (Ctrl+C, SIGTERM) are left *pending*: nothing is recorded,
  so the next run claims them again.
* **Crash between a completed call and its record**: the book is not in the state
  files, so the next run replays the call; the idempotent services answer without
  duplicating anything.

## Retry-failed mode

The `-retry-failed` flag re-enqueues every book recorded in `failed_books.txt` so
the run tries them again. Before orchestrating, `control.FailureRequeuer`
atomically empties the failed file (through the `FailureResetter` port of
`localstate.Store`) and adds the cleared IDs to the candidate list. A crash during
the wipe leaves either the old content or an empty file, never a half-written one.

```bash
./bin/control -ids 1342,84,11 -retry-failed
```

Combine with any book selection flag; the re-enqueued IDs are added to the
explicit candidates.

## Verify mode

The `-verify` flag skips orchestration entirely and reconciles `indexed_books.txt`
with the datamart the Search Service exposes. `control.Verifier` asks the
`SearchService` port, implemented by `httpclient.SearchClient`, whether each book
recorded as indexed is published, which the client answers with
`GET /books/{id}`: `200` means published and `404` means the datamart does not
return that book. Each missing book is logged
(`[CONTROL] verify: book missing from datamart`), and when any is missing the run
exits with a non-zero status and the error `verify: N of M indexed books are
missing from the datamart`. Any other answer or a transport error aborts the run
with `check book N in the search service: ...`, because the Search Service itself
is not answering.

```bash
./bin/control -verify -search-url http://localhost:7003
```

The verify mode needs no book IDs and does not wait for the Ingestion or Indexing
Services; it only contacts the Search Service, with the `-timeout` limit per call.
Use it after a run to confirm every indexed book is searchable.

## Coordination metrics

Every finished step is logged with its wall-clock duration
(`[CONTROL] ingest step finished` / `[CONTROL] index step finished`), and the run
summary adds the coordination cost of the whole run:

```
[CONTROL] run finished indexed=2 skipped=0 failed=0 pending=0 elapsed=... books_per_second=...
         ingest_step_total=... ingest_step_avg=... index_step_total=... index_step_avg=...
```

`*_step_avg` spreads each total over the books that entered the pipeline (indexed
plus failed), so the report can compare ingestion cost against indexing cost per
book and justify the worker count with `-workers`.

See [Replicas and Threads Configuration](replicas_and_threads.md) for detailed
guidance on tuning `-workers` and understanding the concurrency model.

## Code layout

The Control Module follows the hexagonal layout of the whole module
([architecture.md](architecture.md#code-structure-hexagonal-layout)). The core
holds the rules and declares every interface it needs; HTTP and the state files
are adapters; `cmd/control` only parses flags and wires.

| Package | Role | Contents |
|---|---|---|
| `internal/control` | Core | `Orchestrator` (worker pool, retries with doubling backoff, transient versus permanent decisions, step metrics), `BookSelector` (hands each candidate to exactly one worker), `Verifier` (`-verify`), `FailureRequeuer` (`-retry-failed`), `ReadinessCheck`, `Discoverer` (`-discover`), and the ports in `ports.go` |
| `internal/control/httpclient` | Driven adapter | `IngestionClient`, `IndexingClient`, `SearchClient`: the HTTP calls, the wire types of the answers (`IndexStatus`, `BookList`) and the mapping of status codes onto the core errors |
| `internal/localstate` | Driven adapter | `Store` and `Load`: the three state files, `flock` / `LockFileEx`, the in-memory sets behind a `sync.RWMutex`, atomic wipe of the failed file |
| `cmd/control` | Composition root and command line | Flags and environment variables, construction of the clients and the store, calls to the use cases, the run summary log line |

Ports of the control core and their adapters:

| Port | Used by | Implemented by |
|---|---|---|
| `IngestionService` (`Ingest`) | `Orchestrator` | `httpclient.IngestionClient` (`POST /ingest/{book_id}`) |
| `IndexingService` (`Update`) | `Orchestrator` | `httpclient.IndexingClient` (`POST /index/update/{book_id}`) |
| `ProgressStore` | `Orchestrator` | `localstate.Store` |
| `ReadinessProbe` (`Ready`) | `ReadinessCheck` | `httpclient.IngestionClient` (`GET /ingest/list`), `httpclient.IndexingClient` (`GET /index/status`) |
| `DatalakeBookLister` (`ListDatalakeBooks`) | `Discoverer` | `httpclient.IngestionClient` (`GET /ingest/list`) |
| `SearchService` (`IsPublished`) | `Verifier` | `httpclient.SearchClient` (`GET /books/{book_id}`) |
| `IndexedBookLister` (`IndexedIDs`) | `Verifier` | `localstate.Store` |
| `FailureResetter` (`WipeFailures`) | `FailureRequeuer` | `localstate.Store` |

The constructors of the control core return `control.ErrMissingDependency` when a
port is missing, and `httpclient/ports.go` and `localstate/ports.go` assert at
compile time that the adapters satisfy their ports. Replacing the state files by a
database in Stage 3 therefore means writing one new adapter for `ProgressStore`,
`IndexedBookLister` and `FailureResetter`; the orchestrator does not change.

## Tests

```bash
go test ./internal/control/... ./internal/localstate/ ./cmd/control/
go test -race ./internal/control/... ./internal/localstate/
go test -bench . -benchmem ./internal/localstate/
```

The core tests (`internal/control`) exercise the use cases against in-memory
fakes of their ports: the orchestration loop (happy path, resume from downloaded
state, retries, permanent failures, shutdown, concurrent workers, step-duration
metrics), the book selector, verify, retry-failed, readiness and discovery. The
HTTP clients are tested separately against `httptest` servers
(`internal/control/httpclient`). The state files are tested against temporary
directories in `internal/localstate`, including
`TestStoreKeepsTheProgressOfAnOrchestratorRunAcrossRestarts`, which runs a real
orchestrator across a reload of the store. `cmd/control/main_test.go` pins the
flag handling, such as `-verify` working without book IDs. The microbenchmarks
measure the control-state append and load costs (the Go substitute for JMH) and
live in `internal/localstate` (`BenchmarkRecordDownloaded`,
`BenchmarkRecordDownloadedConcurrent`, `BenchmarkLoadState`).

## Service contracts used by the Control Module

* `POST /ingest/{book_id}` → `200 {"book_id":1342,"status":"downloaded","path":"..."}`
  (also answers `available` / `unavailable` / `failed`); `404 book_unavailable`
  is permanent, `503 download_failed` is transient.
* `GET /ingest/list` → `{"count":3,"books":[5,17,1342]}`; readiness check of the
  Ingestion Service and source of the `-discover` candidates.
* `POST /index/update/{book_id}` → `200 {"book_id":1342,"index":"updated"}`;
  `404 book_not_found` is permanent (the book must be ingested first).
* `GET /index/status` → index statistics; readiness check of the Indexing Service.
* `GET /books/{book_id}` → book metadata from the datamart; used by `-verify` mode
  to reconcile indexed books; `200` means the book is searchable, `404` means it
  is missing from the datamart.
