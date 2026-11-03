# Boogle Engine — Stage 2

Boogle Engine is the search engine of the *Big Data* course. Stage 2 extends the Stage 1 data
layer into a service-oriented architecture: three cooperating microservices with REST APIs —
**Ingestion** (downloads books from Project Gutenberg into the datalake), **Indexing**
(extracts metadata and builds the inverted index in the MongoDB datamart) and **Search**
(the front-end-facing query API) — coordinated by a **Control Module** through classical HTTP
orchestration. All processes run on a single host; every address, port and data path is
configurable so the system can move to the Stage 3 cluster without changes.

The assignment template prescribes Java with Javalin; this implementation uses Go, the stack
selected by the Stage 1 benchmarks. Section 6 of the report justifies the deviation.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/ingestion`, `cmd/indexing`, `cmd/search`, `cmd/control` | Entry points of the four processes |
| `internal/` | Private logic: crawler, datalake, tokenizer, indexer, MongoDB datamart, HTTP clients, orchestration |
| `api/` | OpenAPI specifications (Swagger 2.0, YAML and JSON) generated from the code annotations |
| `docs/` | Runbooks, architecture diagrams, replica/thread notes and benchmark evidence |
| `scripts/` | End-to-end and API validation scripts |
| `test/load/` | k6 load benchmarks, corpus seeder, resource samplers |

## Prerequisites

| Tool | Version | Needed for | Verify |
|---|---|---|---|
| Go | 1.22+ | Building and testing every service | `go version` |
| Docker + Compose v2 | any recent | Running the whole stack, including MongoDB 7 | `docker compose version` |
| Git | any recent | Cloning the repository | `git --version` |
| k6 | v2.x | Load benchmarks (optional) | `k6 version` |
| Python 3 with pandas and matplotlib | any recent | Regenerating the report figures (optional) | `python3 --version` |

## Build

A single multi-stage Dockerfile builds any of the four images:

```bash
docker compose up -d --build     # builds ingestion, indexing, search and control images
```

Native binaries, to run the services outside Docker:

```bash
go build -o bin/ingestion ./cmd/ingestion
go build -o bin/indexing   ./cmd/indexing
go build -o bin/search     ./cmd/search
go build -o bin/control    ./cmd/control
```

## Running everything on one machine

```bash
docker compose up -d --build                        # MongoDB, ingestion, indexing, search, nginx
docker compose ps                                   # wait until every service shows "(healthy)"

# Orchestrate the whole datalake through the Control Module (on demand, "tools" profile)
docker compose --profile tools run --rm control -discover

# Or an explicit selection of books, with a worker count
docker compose --profile tools run --rm control -ids 1342,84,11 -workers 4

docker compose down                                 # stop the stack (data volumes survive)
docker compose --profile tools down -v              # stop and delete every data volume
```

nginx is the front door of the Search API and publishes it on `SEARCH_PORT` (7003 by default),
whatever the number of search replicas. The Ingestion and Indexing APIs publish on 7001 and
7002. Each service also serves its Swagger UI at `/swagger/index.html`.

## Configuration

### Environment variables (docker compose)

Copy `.env.example` to `.env` to change any of these; the defaults are shown here.

| Variable | Default | Meaning |
|---|---|---|
| `INGESTION_PORT` | `7001` | Host port of the Ingestion API |
| `INDEXING_PORT` | `7002` | Host port of the Indexing API |
| `SEARCH_PORT` | `7003` | Host port of nginx (the Search API front door) |
| `MONGO_PORT` | `27017` | Host port of MongoDB |
| `SEARCH_REPLICAS` | `1` | Number of Search Service replicas behind nginx |
| `CORS_ALLOWED_ORIGIN` | `*` | Browser origins allowed to call the Search API |
| `MONGO_DATABASE` | `boogle` | Datamart database name (benchmark runs use `boogle_bench`) |

### Service flags

Every service reads its listen address and paths from flags; run any binary with `-h` for the
full list.

| Service | Main flags |
|---|---|
| Ingestion | `-addr` (`:7001`), `-datalake`, `-workers` (concurrent downloads, default 8), `-ingest-timeout` |
| Indexing | `-addr` (`:7002`), `-datalake`, `-rebuild-timeout` (default 10m), `-max-concurrent-updates` |
| Search | `-addr` (`:7003`); database and CORS come from the environment |
| Control | `-ingestion-url`, `-indexing-url`, `-search-url`, `-state-dir`, `-workers` (default 4), `-ids`/`-from`/`-to`/`-discover`, `-retry-failed`, `-verify` |

### Replicas and threads

- **Replicas:** `SEARCH_REPLICAS=2` (or `docker compose up -d --scale search=2`) runs several
  Search replicas behind nginx; `docs/replicas_and_threads.md` documents the replica and thread
  table of every service and what changes in Stage 3.
- **Threads:** each HTTP request is handled by one goroutine; the Control Module's pipeline
  concurrency is `-workers`, the Ingestion download cap is `-workers`. `GOMAXPROCS` (default:
  the number of CPU cores) sets the runtime thread pool.

### Data paths

Containers carry code only; every dataset lives in a named volume: `datalake` (book headers and
bodies), `control-state` (the Control Module's progress files) and `mongo-data` (MongoDB). The
paths inside the containers (`/data/datalake`, `/data/control`) are set by flags and environment
variables, never hard-coded for the host.

## Test queries

With the stack running, the Search API answers from the datamart:

```bash
curl "http://localhost:7003/search?q=adventure&limit=5"
curl "http://localhost:7003/search?q=adventure&language=en"            # filters combine with AND
curl "http://localhost:7003/books/74"                                   # book metadata
curl "http://localhost:7003/search/filters"                             # available filter values
```

## Tests and validation

```bash
gofmt -l .        # must print nothing
go vet ./...
go test ./...     # MongoDB-backed tests skip fast when no server is reachable
```

Runnable checks against the live services (expected outputs are recorded in `docs/evidence/`):

```bash
scripts/api_validation.sh            # Search API contract checks (keyword, filters, pagination, errors)
scripts/e2e_control.sh               # orchestration, restart, failure and discovery semantics
scripts/e2e_compose.sh               # full compose stack: recovery and volume persistence
```

## Benchmarks

Full methodology and results: [`docs/evidence/README.md`](docs/evidence/README.md); the official
session protocol: [`docs/evidence/benchmark_protocol.md`](docs/evidence/benchmark_protocol.md).

**Microbenchmarks** (component level, `testing.B` aggregated with `benchstat`):

```bash
# Text pipeline over the real books in the datalake
BOOGLE_BENCH_DATALAKE=/path/to/datalake go test -run '^$' -bench . -benchmem \
  -benchtime 1s -count 10 -cpu 1,2,4,8 ./internal/indexer/

# Datamart primitives against a live MongoDB
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/

go run golang.org/x/perf/cmd/benchstat@latest docs/evidence/benchmarks.txt
```

**Load benchmarks** (system level, the stack must be up):

```bash
test/load/seed_corpus.sh 300                        # seed a datamart of at least 300 books
k6 run test/load/indexing_benchmark.js              # POST /index/update at 1/5/10 clients
k6 run test/load/search_benchmark.js                # GET /search at 1/10/50/100 clients

docker compose up -d --scale search=2               # replica experiment: same profile, 2 replicas
k6 run test/load/search_benchmark.js
```

**Resource sampling** (start in a second terminal before each k6 run):

```bash
test/load/sample_containers.sh docs/evidence/search_resources.csv 360   # CPU/memory per container
test/load/sample_host.sh docs/evidence/host_search.csv 360              # whole-host CPU/memory
```

**Report figures** from the recorded results:

```bash
python3 scripts/plot_benchmarks.py                  # writes docs/evidence/figures/ (PNG + PDF)
```

## API specifications

The OpenAPI specifications are generated from the Go annotations with `swaggo/swag` and live in
[`api/`](api/):

| Specification | Endpoints |
|---|---|
| `api/ingestion/swagger.yaml` / `.json` | `POST /ingest/{id}`, `GET /ingest/status/{id}`, `GET /ingest/list` |
| `api/indexing/swagger.yaml` / `.json` | `POST /index/update/{id}`, `POST /index/rebuild`, `GET /index/status` |
| `api/search/swagger.yaml` / `.json` | `GET /search`, `GET /books/{id}`, `GET /search/filters` |

Each specification documents the provider, the intended consumers, parameters, response schemas,
status codes, idempotency and completion semantics; front-end-facing, internal and operational
routes are marked with `x-` extension fields. Changing an endpoint requires updating its
annotation and regenerating the specification in the same commit.

Further documentation: [`docs/control.md`](docs/control.md) (Control Module runbook),
[`docs/mongodb.md`](docs/mongodb.md) (datamart design), [`docs/architecture.md`](docs/architecture.md)
(diagrams) and [`docs/replicas_and_threads.md`](docs/replicas_and_threads.md).

## Team

**Group `bigdata-devs`**

| Member | GitHub |
|---|---|
| Tomás Santana Suárez | [@TemiArtemi](https://github.com/TemiArtemi) |
| Carlos Montesdeoca Vega | [@CarlosMontesdeoca21](https://github.com/CarlosMontesdeoca21) |
| Aythami Lorenzo Padilla | [@aythamilorenzo](https://github.com/aythamilorenzo) |
| Javier Ruano Hernández | [@javierruanohdez](https://github.com/javierruanohdez) |
| Alejandro Delgado Valera | [@aledelgadoo](https://github.com/aledelgadoo) |
