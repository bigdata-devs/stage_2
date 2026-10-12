# Control Module Runbook

The Control Module is the classical orchestrator of Boogle Engine. It drives the
pipeline book by book over HTTP — ingestion first, indexing second — and records
every completed step on disk so a restarted run resumes instead of duplicating work.

## Build and run

```bash
go build -o bin/control ./cmd/control
./bin/control -ids 1342,84,11 -workers 4
./bin/control -from 1 -to 500
```

The module refuses to start when a required service is unhealthy: it pings
`GET /health` on both services before the first book is claimed.

## Configuration

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-ingestion-url` | `INGESTION_URL` | `http://localhost:7001` | Base URL of the Ingestion Service |
| `-indexing-url` | `INDEXING_URL` | `http://localhost:7002` | Base URL of the Indexing Service |
| `-state-dir` | `CONTROL_STATE_DIR` | `control` | Folder of the control state files |
| `-timeout` | — | `30s` | Time limit of one service call |
| `-workers` | — | `4` | Concurrent pipeline workers |
| `-attempts` | — | `3` | Attempts per step on transient errors |
| `-backoff` | — | `1s` | Wait before the first retry (doubles each time) |
| `-ids` | — | — | Comma-separated book IDs |
| `-from`, `-to` | — | — | Inclusive range of book IDs |

Book selection requires `-ids` and/or `-from`/`-to`. Flags override environment
variables; both endpoints are configurable so no `localhost` address is hard-coded.

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

## Tests

```bash
go test ./internal/control/...
go test -race ./internal/control/...
```

The tests exercise the orchestration loop against fake services: happy path,
resume from downloaded state, retries, permanent failures, shutdown, and concurrent
workers. The HTTP clients are tested separately against `httptest` servers.

## Service contracts used by the Control Module

* `POST /ingest/{book_id}` → `{"book_id":1342,"status":"downloaded","path":"..."}`
  (also accepts `available` / `unavailable` / `failed`)
* `POST /index/update/{book_id}` → `{"book_id":1342,"index":"updated"}`
* `GET /health` on both services (status code only, body free-form)
