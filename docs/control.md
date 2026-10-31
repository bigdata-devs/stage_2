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
./bin/control -ids 1342,84 -retry-failed
./bin/control -verify -search-url http://localhost:7003
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

## Retry-failed mode

The `-retry-failed` flag re-enqueues every book recorded in `failed_books.txt` so
the run tries them again. Before orchestrating, the module atomically empties the
failed file and adds the cleared IDs to the candidate list. A crash during the
wipe leaves either the old content or an empty file, never a half-written one.

```bash
./bin/control -ids 1342,84,11 -retry-failed
```

Combine with any book selection flag; the re-enqueued IDs are added to the
explicit candidates.

## Verify mode

The `-verify` flag skips orchestration entirely and reconciles `indexed_books.txt`
with the datamart the Search Service exposes. For every book recorded as indexed,
the module issues `GET /books/{id}` and reports which ones the datamart actually
returns. A `404` means the datamart lost that record; any other non-`2xx` answer
or transport error aborts the run because the Search Service itself is not
reachable.

```bash
./bin/control -verify -search-url http://localhost:7003
```

The verify mode does not wait for the Ingestion or Indexing Services; it only
contacts the Search Service. Use it after a run to confirm every indexed book is
searchable.

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
* `GET /books/{book_id}` → book metadata from the datamart; used by `-verify` mode
  to reconcile indexed books; `200` means the book is searchable, `404` means it
  is missing from the datamart.
