# Stage 2 Evidence — MongoDB Datamart (Member 4)

Captured on 2026-10-18 from branch `feature/mongodb-datamart` (HEAD `6cc1e85`), extended on
2026-10-19 from branch `feature/indexing-service` with the Index Lead deliverables (Indexing
Service bridge, catalog endpoints, load benchmark).

> **PROVISIONAL — every timing number in this folder.** The Go benchmark files and the k6 runs
> were captured on development machines while the code was still moving; the banners at the top
> of the benchmark files repeat the warning. None of it may be cited in the report: the official
> session on the Stage-1 host, run per [`benchmark_protocol.md`](benchmark_protocol.md), replaces
> the numbers in one atomic commit and drops the banners.

| File | Contents |
|---|---|
| [`tests.txt`](tests.txt) | Verbose run of the full `internal/mongodb` suite: **34 PASS** (30 tests + 4 subtests), **0 skipped, 0 failed**, `go test -v -count=1 ./internal/mongodb/` against the live `mongo:7` container. |
| [`benchmarks.txt`](benchmarks.txt) | Raw `go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/` output: four benchmarks × ten samples (300-book corpus, one second or more per sample). |
| [`benchstat.txt`](benchstat.txt) | The same run aggregated with `benchstat` (medians with 95 % confidence intervals, plus `B/op` and `allocs/op`). |
| [`benchstat.csv`](benchstat.csv) | The same aggregated table in machine-readable CSV (`benchstat -format csv`), the format §6.3 asks for. |
| [`benchmark_protocol.md`](benchmark_protocol.md) | Runbook of the official benchmark session: host record to fill in, preconditions, exact commands, replica experiment and the atomic replacement checklist. |
| [`demo.md`](demo.md) | End-to-end transcript: datalake → Indexing Service → datamart → Search Service, with the literal request/response bodies of §1–§4. |
| [`catalog_smoke.md`](catalog_smoke.md) | **2026-10-19.** HTTP transcript of the optional catalog endpoints (`GET /books/{book_id}`, `GET /search/filters`) plus the fail-fast start-up against an unreachable datamart. |
| [`indexing_benchmark_2026-10-19T19-07-00-230Z.csv`](indexing_benchmark_2026-10-19T19-07-00-230Z.csv) | **2026-10-19.** k6 steady-state table per concurrency level: books indexed per second, latency percentiles, error rate. |
| [`indexing_benchmark_2026-10-19T19-07-00-230Z.json`](indexing_benchmark_2026-10-19T19-07-00-230Z.json) | The complete k6 summary of that run (every metric, check and threshold), for plotting. |
| [`indexing_benchmark_run.log`](indexing_benchmark_run.log) | Console transcript of the same run, including the threshold report. |
| [`indexing_resources.csv`](indexing_resources.csv) | 1-second samples of the Indexing Service (CPU %, working set, private memory, threads) and of MongoDB in Docker (CPU %, memory) taken while k6 drove the load. |
| [`microbenchmarks.txt`](microbenchmarks.txt) | **2026-10-28.** Raw `testing.B` output of `ProcessText`, `ExtractMetadata` and `SplitBookText` over the 20 real books with `-cpu 1,2,4,8` (review feedback). |
| [`e2e_control_2026-10-30T10-09-40Z.txt`](e2e_control_2026-10-30T10-09-40Z.txt) | **2026-10-30.** Transcript of `scripts/e2e_control.sh` after restoring `cmd/ingestion/main.go` (RF 2): all orchestration, restart, permanent-failure and discovery checks PASS against real Gutenberg downloads. |
| [`e2e_compose_2026-10-30T11-16-16Z.txt`](e2e_compose_2026-10-30T11-16-16Z.txt) | **2026-10-30.** Transcript of `scripts/e2e_compose.sh` on Docker Desktop 4.97 (Engine 29.9.2, WSL2): stack build, orchestration, idempotent replay, front-facing search, ingestion-container failure recovery and named-volume persistence all PASS. |
| [`ingestion_throughput.md`](ingestion_throughput.md) | **2026-10-30.** Cold-datalake ingestion throughput on 100 real Gutenberg books at `-workers 1, 2, 4, 8` with CPU/RAM samples (2xx-only counting), per the Stage 2 audit. |
| [`api_validation.txt`](api_validation.txt) | Historical API validation checklist of the Ingestion Service (moved from `cmd/ingestion/results/`). |

## Environment

- Windows 11, Go `go1.27.0` (module targets `go1.25.0`), `goarch=amd64`
- MongoDB `7.0.45` in Docker (`boogle-mongo-test`, port `27017`, database `boogle`)
- CPU: Intel Core i7-14700HX (benchmarks report `GOMAXPROCS=28`)
- RAM: 32 GB physical (31.7 GB usable); the Docker Desktop VM that runs MongoDB is capped at ≈16 GB and 28 CPUs, which bounds what the database can use
- Storage: Samsung MZAL81T0HFLB NVMe SSD, 954 GB (the machine's only internal drive)
- k6 `v2.2.0` (winget `GrafanaLabs.k6`) for the load benchmark

## Reproducing

```bash
docker start boogle-mongo-test                              # live tests skip without a server
gofmt -l . && go vet ./... && go test ./...                 # repository verification

go test -v -count=1 ./internal/mongodb/ > docs/evidence/tests.txt   # §tests
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/   # §benchmarks
go run golang.org/x/perf/cmd/benchstat@latest <benchmarks-output>                  # §benchstat
go run golang.org/x/perf/cmd/benchstat@latest -format csv <benchmarks-output> > docs/evidence/benchstat.csv  # §benchstat CSV
# PowerShell: wrap the whole command in cmd /c "..." so ">" writes bytes, not UTF-16
```

The demo additionally needs the two bridge files and the wiring from
[`docs/mongodb.md`](../mongodb.md) (they belong to the services of Members 3 and 5), the four-book
corpus described at the top of `demo.md`, and both services started as shown there. On this
platform benchmark samples must span whole seconds — the monotonic clock reads sub-millisecond
spans as zero — which the `-benchtime 1s` window and the ten repetitions guarantee.

## Benchmark methodology (Go `testing.B` instead of JMH)

The assignment suggests JMH; these benchmarks use Go's native `testing.B` framework aggregated
with `benchstat`, which the project conventions designate as the Go substitute for JMH. The
methodology is equivalent — adaptive iteration calibration (warm-up), ten repetitions with 95 %
confidence intervals, and `ns/op` + `B/op` + `allocs/op` metrics — while keeping measurement
inside the process under study: driving the services through HTTP to satisfy JMH's JVM harness
would time process scheduling and JSON marshalling instead of the datamart operations.

## Indexing load benchmark — `POST /index/update` (Index Lead)

`test/load/indexing_benchmark.js` drives the Indexing Service through HTTP with k6, because this
benchmark is about the service as a whole (HTTP + tokenizer + postings writes), not about one
datamart primitive. One successful update indexes one book, so throughput is reported directly in
**books per second**.

### Workload

- Corpus: 20 real Project Gutenberg books (`11,26,43,46,74,76,84,98,201,521,844,1080,1260,1342,
  1355,1400,1661,17489,1952,2701`), downloaded once into `./datalake` with the Ingestion Service
  and warm-up indexed before the measured run, so the numbers are **steady-state re-indexing**.
- Closed model, stepped profile: 1 → 5 → 10 VUs, ramp 10 s, hold 15 s each, think time 0, so the
  VU count equals the client concurrency. Only hold phases are reported.
- Seed 2026; every VU walks the corpus from its own offset. Re-indexing is idempotent, so the
  corpus may be smaller than the number of requests.

### Results (2026-10-19, `MONGO_DATABASE=boogle_bench`)

| VUs | books | books/s | avg ms | p95 ms | p99 ms | errors |
|----:|------:|--------:|-------:|-------:|-------:|-------:|
| 1 | 12 | 0.80 | 1237 | 1965 | 2334 | 0 % |
| 5 | 51 | 3.40 | 1383 | 2926 | 3462 | 0 % |
| 10 | 92 | 6.13 | 1667 | 3434 | 4554 | 0 % |

Whole run: 267 successful updates, **0 % errors**, avg 1543 ms, p95 3123 ms. The load profile
scales sub-linearly (10× the concurrency buys ≈7.7× the throughput) and the run exits `99`
because the SLO thresholds (`avg<1500`, `p95<3000`, and `p95<3000` inside the 10-VU hold) are
crossed — an honest saturation point, not a script failure: all availability thresholds
(`http_req_failed<0.01`, `checks>0.99`) pass.

### Resource sample during the run (`indexing_resources.csv`)

| Metric | Value |
|---|---|
| Indexing Service CPU | 0 – 13.9 % (avg 6.2 % of 28 threads ≈ 1.7 cores) |
| Indexing Service working set | 39 MB → 156 MB |
| Indexing Service threads | ≤ 17 |
| MongoDB CPU (Docker) | up to **838.7 %** (≈ 8 cores), ~500–800 % under sustained load |
| MongoDB memory | ~140 MiB |

The service is not CPU-bound — MongoDB absorbs the write amplification of every book's postings,
which is where the throughput ceiling comes from. The pprof profiles of a load run can be pulled
from the dedicated port (`-pprof :7012`) without touching the API port.

### Reproducing the load benchmark

```bash
go build -o bin/ingestion.exe ./cmd/ingestion
go build -o bin/indexing.exe ./cmd/indexing

# 1. corpus (once): download the 20 books with the Ingestion Service
bin/ingestion.exe -addr :7016 -datalake datalake &
for id in 11 26 43 46 74 76 84 98 201 521 844 1080 1260 1342 1355 1400 1661 17489 1952 2701; do
  curl -X POST http://localhost:7016/ingest/$id
done

# 2. service with the profiler port, on an isolated database
MONGO_DATABASE=boogle_bench bin/indexing.exe -addr :7015 -datalake datalake -pprof :7012 &

# 3. warm-up indexation (first-time indexing is not part of the measured state)
for id in 11 26 43 46 74 76 84 98 201 521 844 1080 1260 1342 1355 1400 1661 17489 1952 2701; do
  curl -X POST http://localhost:7015/index/update/$id
done

# 4. resource sampler and load test, in two terminals
powershell -File test/load/sample_resources.ps1 -ProcessName indexing -DurationS 150 -DockerContainer boogle-mongo
BASE_URL=http://localhost:7015 HOLD_DURATION=15s k6 run test/load/indexing_benchmark.js
```

k6 writes `<stamp>.csv` and `<stamp>.json` into `test/load/results/` (create the folder first)
and prints the table above. A k6 exit code of `99` only means a threshold was crossed — read the
"Thresholds" block of the log to see which.

## Microbenchmarks of the text pipeline (review feedback)

`internal/indexer/benchmark_test.go` measures the three per-book primitives of indexing on the
same 20-book corpus, cycling it once per iteration with one cursor per goroutine and `SetBytes`
sized to the average item. `SplitBookText` rebuilds the raw Gutenberg layout around the stored
header and body, because the datalake keeps them already split.

```bash
go test ./internal/indexer -run '^$' -bench . -benchmem -cpu 1,2,4,8 -benchtime=500ms
```

| Benchmark | 1 worker | 8 workers | Speed-up | Allocations |
|---|---:|---:|---:|---:|
| `ProcessText` (body) | 21.6 ms/op, 22.7 MB/s | 5.2 ms/op, 94.0 MB/s | 4.1× | 10.4 MB, 23 allocs |
| `ExtractMetadata` (header) | 64 µs/op, 13.1 MB/s | 11 µs/op, 76.3 MB/s | 5.8× | 3.5 kB, 46 allocs |
| `SplitBookText` (raw book) | 0.88 ms/op, 560 MB/s | 0.12 ms/op, 4.2 GB/s | 7.5× | 0 kB, 0 allocs |

Run on 2026-10-28 on the Index Lead's machine (i7-1355U, 12 logical CPUs). Tokenization dominates
the CPU cost and allocates ~10 MB per average book, which is why the rebuild now streams one book
at a time instead of collecting every token stream first; header parsing and marker splitting are
negligible. Raw output in [`microbenchmarks.txt`](microbenchmarks.txt).
