# Benchmark Protocol — Official Stage 2 Session

This is the runbook for the **official** benchmark session. The numbers currently in this folder
were captured on a development laptop while the code was still moving; they are marked
**PROVISIONAL** and must not be cited in the report. The official session replaces them atomically
in one commit, on the Stage-1 host, executed by the Index Lead. Budget roughly **4–6 hours**.

Everything is driven from the repository root. Commands are shown for a POSIX shell; on PowerShell
wrap redirections in `cmd /c "..."` (see the warning in [`README.md`](README.md)) so files are
written as UTF-8 bytes, not UTF-16.

## 1. Host record (fill in before measuring)

| Property | Value |
|---|---|
| Date of the session | |
| Git commit (`git rev-parse HEAD`) | |
| CPU model, cores / threads | |
| `GOMAXPROCS` reported by the benchmarks | |
| RAM (total, and Docker Desktop VM cap) | |
| Storage (model, type, free space) | |
| OS and version | |
| Go version (`go version`) | |
| MongoDB server version (`mongosh --eval 'db.version()'`) | |
| Docker VM CPUs / memory | |
| k6 version (`k6 version`) | |
| Power plan / load on the machine | |

## 2. Preconditions

- [ ] The machine is otherwise idle, on AC power, performance power plan.
- [ ] Docker resources are set (record them above); the container runs `mongo:7`.
- [ ] `MONGO_URI=mongodb://localhost:27017` (default) reaches a **fresh** server; tests and
      microbenchmarks use database `boogle`, each k6 run uses a fresh `boogle_bench`.
- [ ] The datalake holds the 20-book corpus
      (`11,26,43,46,74,76,84,98,201,521,844,1080,1260,1342,1355,1400,1661,17489,1952,2701`),
      ingested once with the Ingestion Service.
- [ ] `go`, `k6` and `mongosh` on PATH; run from the repository root.
- [ ] One warm-up pass ran and its output was discarded (JIT-free languages still benefit: page
      cache, connection pools, MongoDB WiredTiger cache warm).

## 3. Functional tests → `tests.txt`

```bash
go test -count=1 -v ./... > docs/evidence/tests.txt
```

With the server up the expected result is every package `ok` and **0 skipped**; the live MongoDB
suites (`internal/mongodb`, `cmd/indexing`) fail loudly here if the server is unreachable — the
TestMain probe only makes them *skip* fast, which is a smoke condition, not an official one.

## 4. Go microbenchmarks (`testing.B`, ~15 min)

Why `testing.B` instead of JMH: see “Benchmark methodology” in [`README.md`](README.md).

```bash
# Text pipeline over the real books, GOMAXPROCS swept (review feedback) → microbenchmarks.txt
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 -cpu 1,2,4,8 ./internal/indexer/ \
  > docs/evidence/microbenchmarks.txt

# Datamart primitives on the live server, machine-default GOMAXPROCS → benchmarks.txt
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/ \
  > docs/evidence/benchmarks.txt

# The same suite with the thread sweep → benchmarks_cpu.txt (report section 7: threads)
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 -cpu 1,2,4,8 ./internal/mongodb/ \
  > docs/evidence/benchmarks_cpu.txt
```

`./internal/mongodb/` runs five benchmarks against the live server: `PostingsForTerms`,
`SearchBooksWithoutFilters`, `SearchBooksWithAllFilters`, `IndexBook` (300-book corpus) and
`IndexBookRealBook` (book 1342 from `testdata/`, which also reports `terms/book` and
`tokens/book` as custom metrics). No datalake is needed for this step.

Aggregate each file with benchstat (medians, 95 % CI, `B/op`, `allocs/op`):

```bash
go run golang.org/x/perf/cmd/benchstat@latest docs/evidence/benchmarks.txt \
  > docs/evidence/benchstat.txt
go run golang.org/x/perf/cmd/benchstat@latest -format csv docs/evidence/benchmarks.txt \
  > docs/evidence/benchstat.csv
go run golang.org/x/perf/cmd/benchstat@latest docs/evidence/benchmarks_cpu.txt \
  > docs/evidence/benchstat_cpu.txt
```

## 5. Load benchmarks (k6)

### 5.1 Indexing — `POST /index/update`

The script documents itself (`test/load/indexing_benchmark.js`): stepped VU levels
(`VU_LEVELS`, default `1,5,10`), hold phases only, seeded corpus walk, SLO thresholds.

```bash
export MONGO_DATABASE=boogle_bench
go run ./cmd/indexing -addr :7002 -datalake datalake -rebuild-timeout 15m   # terminal 1
RESULTS_DIR=test/load/results k6 run test/load/indexing_benchmark.js > docs/evidence/indexing_benchmark_run.log
```

Copy the timestamped `.json` and `.csv` that the script writes under `test/load/results/` into
`docs/evidence/`, and sample resources during the run (Index Lead's sampler, committed):

```powershell
powershell -File test/load/sample_resources.ps1   # writes docs/evidence/indexing_resources.csv
```

### 5.2 Search — `GET /search`

Owned by the Search role together with RF1 (adapter + `test/load/search_benchmark.js`). Run it
with the same discipline: record VU levels, SLOs and the fresh `boogle_bench` database, and save
JSON, CSV and console log next to the indexing outputs.

### 5.3 Search replicas (audit RF 8)

With the Search role's compose override (two Search replicas behind nginx), repeat the search
profile of 5.2 unchanged and keep both runs side by side; the only variable is the replica count.

## 6. Replica experiment — concurrent rebuilds across two Indexing replicas

The rebuild lease (`internal/mongodb/rebuild_lock.go`) makes `POST /index/rebuild` safe with any
number of replicas. Verify it live:

```bash
export MONGO_DATABASE=boogle_bench
go run ./cmd/indexing -addr :7002 -datalake datalake -rebuild-timeout 15m   # replica A
go run ./cmd/indexing -addr :7003 -datalake datalake -rebuild-timeout 15m   # replica B
curl -X POST http://localhost:7002/index/rebuild &
curl -X POST http://localhost:7003/index/rebuild
```

Expected: one request answers `200` with the rebuild report (`books_processed`, elapsed), the
overlapping one answers `409` with `{"error": ...}`; the lease renews in the background, survives
network hiccups and is deleted on release, so a retry after the first rebuild finishes succeeds.
Record both requests and responses with timestamps in `docs/evidence/rebuild_replica.md`.

## 7. Thread and replica variables (report section 7 evidence)

| Variable | Default today | How this session varies it |
|---|---|---|
| Replicas per service | 1 (ingestion, indexing, search) | Search ×2 behind nginx (5.3); Indexing ×2 only in the rebuild experiment (6) |
| `GOMAXPROCS` | runtime `NumCPU` of the host | `-cpu 1,2,4,8` sweep (4) |
| Goroutines | one per HTTP connection (`net/http`) | not varied; bounded in practice by k6 VUs |
| MongoDB driver pool | driver default `MaxPoolSize` 100 per process | not varied; replica runs double the pools, which stays below the server's ceiling at this scale |
| Client concurrency (k6 VUs) | `1,5,10` (indexing), search profile (Search role) | already part of each load run |
| Rebuild concurrency | one lease holder cluster-wide | enforced by experiment 6 |

## 8. Smoke variants — discardable

Quick validation of the protocol itself (for example on a laptop before travelling). **Never copy
smoke output into `docs/evidence/` and never cite it** — it cannot support a claim:

```bash
go test -run '^$' -bench . -benchmem -benchtime 10x -count 2 ./internal/mongodb/
HOLD_DURATION=10s VU_LEVELS=1 k6 run test/load/indexing_benchmark.js
```

## 9. Replacement checklist (single commit at the end)

- [ ] Replace: `tests.txt`, `benchmarks.txt`, `benchstat.txt`, `benchstat.csv`,
      `microbenchmarks.txt`, the k6 `.json`/`.csv`/`.log` outputs, `indexing_resources.csv`.
- [ ] Add: `benchmarks_cpu.txt`, `benchstat_cpu.txt`, `rebuild_replica.md`, the search outputs.
- [ ] Drop the **PROVISIONAL** banners from the files this session superseded.
- [ ] Fill the host table of section 1 and the variables table of section 7.
- [ ] Update the file table and the Environment section of [`README.md`](README.md) with the
      official host and the new dates.
- [ ] One atomic commit: `docs: record the official benchmark session`.
