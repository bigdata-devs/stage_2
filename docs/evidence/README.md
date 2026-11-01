# Stage 2 Evidence — MongoDB Datamart (Member 4)

Captured on 2026-10-18 from branch `feature/mongodb-datamart` (HEAD `6cc1e85`), extended on
2026-10-19 from branch `feature/indexing-service` with the Index Lead deliverables (Indexing
Service bridge, catalog endpoints, load benchmark), and **replaced on 2026-11-01 by the official
benchmark session** run per [`benchmark_protocol.md`](benchmark_protocol.md) on the Stage-1 host
(commit `ade7245`, `develop`). The timing files in this folder are the official numbers.

| File | Contents |
|---|---|
| [`tests.txt`](tests.txt) | **2026-11-01 (official).** Verbose `go test -count=1 -v ./...` against the live `mongo:7` container: **365 PASS, 0 skipped, 0 failed**, every package `ok`. |
| [`benchmarks.txt`](benchmarks.txt) | **2026-11-01 (official).** Raw `go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/`: five benchmarks × ten samples (300-book corpus seeded through the real write path per sample). |
| [`benchstat.txt`](benchstat.txt) | The same run aggregated with `benchstat` (medians with 95 % confidence intervals, plus `B/op` and `allocs/op`). |
| [`benchstat.csv`](benchstat.csv) | The same aggregated table in machine-readable CSV (`benchstat -format csv`), the format §6.3 asks for. |
| [`benchmarks_cpu.txt`](benchmarks_cpu.txt) | **2026-11-01 (official).** The same datamart suite swept with `-cpu 1,2,4,8` (report section 7: threads). |
| [`benchstat_cpu.txt`](benchstat_cpu.txt) | That sweep aggregated with `benchstat`. |
| [`microbenchmarks.txt`](microbenchmarks.txt) | **2026-11-01 (official).** Raw `testing.B` output of `ProcessText`, `ExtractMetadata` and `SplitBookText` over the 383-book datalake copy with `-cpu 1,2,4,8`, ten samples each. |
| [`benchmark_protocol.md`](benchmark_protocol.md) | Runbook of the official benchmark session: host record (filled in), preconditions, exact commands, replica experiment and the atomic replacement checklist. |
| [`indexing_benchmark_2026-11-01T16-14-15-954Z.csv`](indexing_benchmark_2026-11-01T16-14-15-954Z.csv) | **2026-11-01 (official).** k6 steady-state table per concurrency level for `POST /index/update`: books indexed per second, latency percentiles, error rate. |
| [`indexing_benchmark_2026-11-01T16-14-15-954Z.json`](indexing_benchmark_2026-11-01T16-14-15-954Z.json) | The complete k6 summary of that run (every metric, check and threshold), for plotting. |
| [`indexing_benchmark_run.log`](indexing_benchmark_run.log) | Console transcript of the same run, including the threshold report. |
| [`indexing_resources.csv`](indexing_resources.csv) | **2026-11-01 (official).** 2-second samples of the Indexing Service and MongoDB containers (CPU %, memory, PIDs) taken with `test/load/sample_containers.sh` while k6 drove the load. |
| [`search_benchmark_2026-11-01T16-26-57-365Z_1replica.csv`](search_benchmark_2026-11-01T16-26-57-365Z_1replica.csv) | **2026-11-01 (official).** k6 `GET /search` with **one** Search replica behind nginx: throughput, latency percentiles and error rate per VU level. |
| [`search_benchmark_2026-11-01T16-26-57-365Z_1replica.json`](search_benchmark_2026-11-01T16-26-57-365Z_1replica.json) | The complete k6 summary of that run. |
| [`search_benchmark_run_1replica.log`](search_benchmark_run_1replica.log) | Console transcript of the same run. |
| [`search_benchmark_2026-11-01T16-30-41-872Z_2replicas.csv`](search_benchmark_2026-11-01T16-30-41-872Z_2replicas.csv) | **2026-11-01 (official).** The same profile repeated with **two** Search replicas behind nginx (the only variable is the replica count). |
| [`search_benchmark_2026-11-01T16-30-41-872Z_2replicas.json`](search_benchmark_2026-11-01T16-30-41-872Z_2replicas.json) | The complete k6 summary of that run. |
| [`search_benchmark_run_2replicas.log`](search_benchmark_run_2replicas.log) | Console transcript of the same run. |
| [`search_resources.csv`](search_resources.csv) | **2026-11-01 (official).** Container samples (Search, nginx, MongoDB) during the one-replica run. |
| [`search_resources_2replicas.csv`](search_resources_2replicas.csv) | **2026-11-01 (official).** Container samples (both Search replicas, nginx, MongoDB) during the two-replica run. |
| [`rebuild_replica.md`](rebuild_replica.md) | **2026-11-01 (official).** Cross-replica rebuild lease experiment: concurrent `POST /index/rebuild` on two Indexing replicas → `200` + `409 rebuild_in_progress`, retry succeeds after the lease is released. |
| [`benchmark_protocol.md`](benchmark_protocol.md) | Runbook of the official benchmark session (host record filled in, preconditions, commands, replacement checklist). |
| [`demo.md`](demo.md) | End-to-end transcript: datalake → Indexing Service → datamart → Search Service, with the literal request/response bodies of §1–§4. |
| [`catalog_smoke.md`](catalog_smoke.md) | **2026-10-19.** HTTP transcript of the optional catalog endpoints (`GET /books/{book_id}`, `GET /search/filters`) plus the fail-fast start-up against an unreachable datamart. |
| [`e2e_control_2026-10-30T10-09-40Z.txt`](e2e_control_2026-10-30T10-09-40Z.txt) | **2026-10-30.** Transcript of `scripts/e2e_control.sh` after restoring `cmd/ingestion/main.go` (RF 2): all orchestration, restart, permanent-failure and discovery checks PASS against real Gutenberg downloads. |
| [`e2e_compose_2026-10-30T11-16-16Z.txt`](e2e_compose_2026-10-30T11-16-16Z.txt) | **2026-10-30.** Transcript of `scripts/e2e_compose.sh` on WSL2 with Docker Engine 29: stack build, orchestration, idempotent replay, front-facing search, ingestion-container failure recovery and named-volume persistence all PASS. |
| [`ingestion_throughput.md`](ingestion_throughput.md) | **2026-10-30.** Cold-datalake ingestion throughput on 100 real Gutenberg books at `-workers 1, 2, 4, 8` with CPU/RAM samples (2xx-only counting), per the Stage 2 audit. |
| [`api_validation.txt`](api_validation.txt) | Historical API validation checklist of the Ingestion Service (moved from `cmd/ingestion/results/`). |
| [`api_validation_results.md`](api_validation_results.md) | **2026-10-31.** Output of `scripts/api_validation.sh` (audit RF 10): **19/19** Search API contract checks, from keyword, filters, empty and past-the-end pages to every 400/404 case, `/books/{book_id}`, `/search/filters`, multi-word AND and CORS preflight, with the literal response bodies. |

## Environment (official session, 2026-11-01)

- Host: WSL2 on Windows, Linux `6.18.33.2-microsoft-standard-WSL2`, Windows power plan set to
  **High performance** for the session
- CPU: Intel Core i7-10750H @ 2.60 GHz, 6 cores / 12 threads (12 vCPUs to WSL2; benchmarks report
  `GOMAXPROCS=12`)
- RAM: 15.8 GB Windows host; 9.7 GB allocated to the WSL2 distro that runs Docker Engine 29.1.3
- Storage: Windows NTFS on `/mnt/c` (1.9 TB) for the repository; WSL2 ext4 root (1007 GB) where the
  Docker volumes (`datalake`, `mongo-data`) live
- Go `go1.27.2 linux/amd64` (module targets `go1.25.0`); MongoDB `7.0.45` (`mongo:7` container);
  k6 `v2.3.0`; Docker Compose `2.40.3`
- Database per run: tests and Go benchmarks use `boogle`; every k6 run uses a fresh `boogle_bench`
  (the compose file reads `MONGO_DATABASE` from the environment, `.env` pins `boogle_bench`)
- Machine otherwise idle, on AC, during every measurement

## Reproducing

```bash
docker compose up -d --build                          # stack + mongo on 27017
go test -count=1 -v ./... > docs/evidence/tests.txt   # §tests (live server required)

# §Go microbenchmarks (the datalake copy feeds internal/indexer)
BOOGLE_BENCH_DATALAKE=/path/to/datalake go test -run '^$' -bench . -benchmem \
  -benchtime 1s -count 10 -cpu 1,2,4,8 ./internal/indexer/ > docs/evidence/microbenchmarks.txt
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/ \
  > docs/evidence/benchmarks.txt
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 -cpu 1,2,4,8 ./internal/mongodb/ \
  > docs/evidence/benchmarks_cpu.txt
go run golang.org/x/perf/cmd/benchstat@latest docs/evidence/benchmarks.txt      # §benchstat
go run golang.org/x/perf/cmd/benchstat@latest -format csv docs/evidence/benchmarks.txt \
  > docs/evidence/benchstat.csv                                                 # §benchstat CSV
go run golang.org/x/perf/cmd/benchstat@latest docs/evidence/benchmarks_cpu.txt  # §benchstat CPU

# §k6 indexing (fresh boogle_bench, 20-book corpus warm-up indexed first)
k6 run test/load/indexing_benchmark.js > docs/evidence/indexing_benchmark_run.log

# §k6 search: seed ≥300 books, then the same profile with 1 and with 2 replicas
test/load/seed_corpus.sh 300
k6 run test/load/search_benchmark.js
docker compose up -d --scale search=2 && k6 run test/load/search_benchmark.js

# Resource sampling in a second terminal (containers, not processes)
test/load/sample_containers.sh docs/evidence/search_resources.csv 360
```

k6 writes `<stamp>.csv` and `<stamp>.json` into `test/load/results/` and prints the summary table.
A k6 exit code of `99` only means a threshold was crossed — read the "Thresholds" block of the log
to see which; all availability thresholds (`http_req_failed<0.01`, `checks>0.99`) passed in every
official run.

## Benchmark methodology (Go `testing.B` instead of JMH)

The assignment suggests JMH; these benchmarks use Go's native `testing.B` framework aggregated
with `benchstat`, which the project conventions designate as the Go substitute for JMH.
The methodology is equivalent — adaptive iteration calibration (warm-up), ten repetitions with 95 %
confidence intervals, and `ns/op` + `B/op` + `allocs/op` metrics — while keeping measurement
inside the process under study: driving the services through HTTP to satisfy JMH's JVM harness
would time process scheduling and JSON marshalling instead of the datamart operations.

## Official results — Go microbenchmarks (2026-11-01)

### Text pipeline (`internal/indexer`, 383 real books, `-cpu 1,2,4,8`, median of 10)

| Benchmark | 1 thread | 8 threads | Speed-up | B/s at 8 |
|---|---:|---:|---:|---:|
| `ProcessText` (body) | 13.66 ms ± 8 % | 3.99 ms ± 6 % | 3.4× | 113 MiB/s |
| `ExtractMetadata` (header) | 23.5 µs ± 10 % | 5.16 µs ± 6 % | 4.6× | 154 MiB/s |
| `SplitBookText` (raw book) | 211 µs ± 2 % | 49.7 µs ± 2 % | 4.3× | 8.9 GiB/s |

Tokenization dominates the per-book CPU cost; header parsing and marker splitting are an order of
magnitude cheaper. All three scale with threads up to 8, so the indexing pipeline itself is not
where the single-machine ceiling comes from.

### Datamart primitives (`internal/mongodb`, live server, 300-book corpus, `GOMAXPROCS=12`)

| Benchmark | sec/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PostingsForTerms` | 603.0 µs ± 5 % | 54.4 Ki | 1 973 |
| `SearchBooksWithoutFilters` | 26.18 ms ± 3 % | 34.1 Ki | 773 |
| `SearchBooksWithAllFilters` | 37.90 ms ± 12 % | 35.6 Ki | 679 |
| `IndexBook` (synthetic 7-token book) | 4.544 ms ± 4 % | 42.4 Ki | 582 |
| `IndexBookRealBook` (book 1342, 6 576 terms) | 636.9 ms ± 2 % | 42.4 Mi | 444.4 k |

`IndexBookRealBook` reports `terms/book = 6 576` and `tokens/book = 58 860`: each real-book index
call rewrites thousands of posting arrays, which is what puts MongoDB at high CPU during indexing.
The thread sweep (`benchmarks_cpu.txt` / `benchstat_cpu.txt`) shows the read path flattening past
a few threads — the server, not the client, is the limit.

## Official results — k6 indexing (2026-11-01, `MONGO_DATABASE=boogle_bench`)

Corpus: the 20 real Gutenberg books, warm-up indexed, so the numbers are steady-state re-indexing.
Closed model, stepped profile 1 → 5 → 10 VUs, ramp 10 s, hold 30 s, think time 0.

| VUs | books | books/s | avg ms | p95 ms | p99 ms | errors |
|----:|------:|--------:|-------:|-------:|-------:|-------:|
| 1 | 33 | 1.10 | 919 | 1 692 | 2 109 | 0 % |
| 5 | 108 | 3.60 | 1 370 | 2 904 | 3 410 | 0 % |
| 10 | 160 | 5.33 | 1 904 | 3 189 | 4 078 | 0 % |

The run exits `99` because the SLO thresholds (`avg<1500`, `p95<3000`) are crossed at the higher
concurrency levels — an honest saturation point, not a script failure: all availability thresholds
pass. Throughput scales sub-linearly (10× the concurrency buys ≈4.8× the throughput).

### Resource sample during the run (`indexing_resources.csv`)

Taken with `test/load/sample_containers.sh` (the WSL2 equivalent of the Windows PowerShell
sampler): one row per container every ~2 s for 170 s.

## Official results — k6 search: 1 vs 2 replicas (2026-11-01)

Same datamart (300 seeded books), same profile, same host: 1 → 10 → 50 → 100 VUs, ramp 10 s,
hold 30 s. The only variable is the replica count behind nginx (`least_conn`, Docker DNS
re-resolution).

| VUs | 1 replica req/s | 2 replicas req/s | 1 repl. avg/p95 ms | 2 repl. avg/p95 ms | errors |
|----:|----------------:|-----------------:|-------------------:|-------------------:|-------:|
| 1 | 47.2 | 49.1 | 21 / 32 | 20 / 30 | 0 % |
| 10 | 246.1 | 245.5 | 40 / 62 | 40 / 63 | 0 % |
| 50 | 246.4 | 242.1 | 202 / 345 | 206 / 356 | 0 % |
| 100 | 249.7 | 247.7 | 400 / 674 | 403 / 680 | 0 % |

**Finding:** two replicas do not raise the ceiling — throughput saturates at ≈250 req/s either way
and latency at 100 VUs is statistically identical. nginx logged real traffic on both upstreams
(`X-Upstream` alternates between the two container IPs), so the load *was* distributed. The
bottleneck therefore lies outside the Search Service process — the shared MongoDB datamart and the
single host — which is exactly the kind of local limit §6.5 asks the report to identify: adding
process replicas cannot fix a storage-side or host-side ceiling, and Stage 3 would need the
datamart itself replicated or sharded.
