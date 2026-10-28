# Control Module Runbook

The Control Module is the classical orchestrator of Boogle Engine. It drives the
pipeline book by book over HTTP — ingestion first, indexing second — and records
every completed step on disk so a restarted run resumes instead of duplicating work.

## Build and run

```bash
go build -o bin/control ./cmd/control
./bin/control -ids 1342,84,11 -workers 4
./bin/control -from 1 -to 500
./bin/control -discover
```

The module refuses to start when a required service is unreachable: it reads
`GET /ingest/list` on the Ingestion Service and `GET /index/status` on the
Indexing Service before the first book is claimed, with a 10 second readiness
deadline.

## Configuration

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-ingestion-url` | `INGESTION_URL` | `http://localhost:7001` | Base URL of the Ingestion Service |
| `-indexing-url` | `INDEXING_URL` | `http://localhost:7002` | Base URL of the Indexing Service |
| `-state-dir` | `CONTROL_STATE_DIR` | `control` | Folder of the control state files |
| `-ingest-timeout` | — | `150s` | Time limit of one `POST /ingest` call, above the service download budget (2 minutes by default) |
| `-timeout` | — | `30s` | Time limit of one indexing, status or readiness call |
| `-workers` | — | `4` | Concurrent pipeline workers |
| `-attempts` | — | `3` | Attempts per step on transient errors |
| `-backoff` | — | `1s` | Wait before the first retry (doubles each time) |
| `-ids` | — | — | Comma-separated book IDs |
| `-from`, `-to` | — | — | Inclusive range of book IDs |
| `-discover` | — | `false` | Add every complete book of the datalake reported by `GET /ingest/list` |

Book selection requires `-ids` and/or `-from`/`-to`, or the `-discover` flag.
Flags override environment variables; both endpoints are configurable so no
`localhost` address is hard-coded.

## Run the whole stack with Docker Compose

`docker-compose.yml` deploys MongoDB, the three services and (on demand) the
Control Module on any machine with Docker:

```bash
docker compose up -d --build                                     # mongo + ingestion + indexing + search
docker compose --profile tools run --rm control -discover        # orchestrate the whole datalake
docker compose --profile tools run --rm control -ids 1342,84     # or an explicit selection
```

The services reach each other by container name (`http://ingestion:7001`,
`http://indexing:7002`) through the environment variables of the compose file —
the same configurability that keeps `localhost` out of the code. Host ports come
from `.env` (copy `.env.example`); the Swagger UIs and the internal status routes
stay reachable on `localhost:7001..7003`.

Containers carry code only: the datalake, the control state files and the
MongoDB datamart live in the named volumes `datalake`, `control-state` and
`mongo-data`. Recreating or killing a container therefore never loses pipeline
state, and a restarted Control Module resumes from `/data/control` exactly as
described in the failure-semantics section below.

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

Every append happens under an exclusive `flock`, so a crash or a second process can
never interleave partial lines. On load, malformed lines are skipped: replaying an
idempotent service call is safe, losing a book ID would not be.

## Failure semantics

* **Transient failures** (network error, timeout, HTTP `408`/`429`/`5xx`, `failed`
  status answers) are retried with a doubling backoff up to `-attempts` times.
* **Permanent failures** (any other HTTP `4xx`, `unavailable` status answers) abort
  retries immediately and are recorded in `failed_books.txt`. To retry such a book
  later, delete its line from the file.
* **Interrupted books** (Ctrl+C, SIGTERM) are left *pending*: nothing is recorded,
  so the next run claims them again.
* **Crash between a completed call and its record**: the book is not in the state
  files, so the next run replays the call; the idempotent services answer without
  duplicating anything.

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

## Tests

```bash
go test ./internal/control/...
go test -race ./internal/control/...
go test -bench . -benchmem ./internal/control/
```

The tests exercise the orchestration loop against fake services: happy path,
resume from downloaded state, retries, permanent failures, shutdown, concurrent
workers and step-duration metrics. The HTTP clients are tested separately against
`httptest` servers, and the microbenchmarks measure the control-state append and
load costs (the Go substitute for JMH).

## Service contracts used by the Control Module

* `POST /ingest/{book_id}` → `200 {"book_id":1342,"status":"downloaded","path":"..."}`
  (also answers `available` / `unavailable` / `failed`); `404 book_unavailable`
  is permanent, `503 download_failed` is transient.
* `GET /ingest/list` → `{"count":3,"books":[5,17,1342]}`; readiness check of the
  Ingestion Service and source of the `-discover` candidates.
* `POST /index/update/{book_id}` → `200 {"book_id":1342,"index":"updated"}`;
  `404 book_not_found` is permanent (the book must be ingested first).
* `GET /index/status` → index statistics; readiness check of the Indexing Service.
