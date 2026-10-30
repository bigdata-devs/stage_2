# Ingestion Throughput Benchmark — Cold Datalake (Ingestion Lead)

Measured on 2026-10-30 with `test/load/ingestion_throughput.ps1` after restoring
`cmd/ingestion/main.go` (RF 2). Every run starts an ingestion service with an
**empty datalake** and drives it over HTTP with `test/load/ingestion_benchmark.go`,
counting **only HTTP 2xx answers as ingested**.

## Environment

- Windows 11 Home, Go `go1.27.2` (module targets `go1.25.0`), `goarch=amd64`
- CPU: Intel Core Ultra 7 155H (16 cores); RAM: 32 GB
- Network: real downloads from `https://www.gutenberg.org/cache/epub`
- Ingestion binary: `go build ./cmd/ingestion`, flags `-addr :7016 -datalake <tmp> -workers N`

## Workload

- Corpus: **59** Gutenberg book IDs (the default list in `ingestion_throughput.ps1`;
  57 valid, 2 consistently answer 404 `book_unavailable` and are excluded from throughput).
- Closed model: the HTTP client concurrency is `max(workers × 2, 10)` so the server
  slots are never starved.
- Cold start: the datalake folder is deleted and recreated before every workers value,
  so every book is downloaded fresh (no `available` shortcuts).
- CPU/RAM sampled at 1 Hz by `test/load/sample_resources.ps1` (process PID of the
  ingestion binary).

## Results (cold datalake, 2xx-only throughput)

| Workers | Ingested (2xx) | Failed (404) | Elapsed | **books/s** | CPU avg | WS max |
|--------:|---------------:|-------------:|--------:|------------:|--------:|-------:|
| 1 | 57 | 2 | 13.21 s | **4.31** | 0.4 % | 81 MB |
| 2 | 57 | 2 | 7.17 s | **7.95** | 0.9 % | 31 MB |
| 4 | 57 | 2 | 4.18 s | **13.64** | 1.0 % | 31 MB |
| 8 | 57 | 2 | 2.86 s | **19.92** | 1.6 % | 32 MB |

Speed-up from 1 → 8 workers: **≈4.6×** (sub-linear; the bottleneck is the upstream
Gutenberg download latency, not local CPU). CPU stays under 2 % of the machine —
the service is I/O-bound on the cold path, which is the expected shape for a
network-bound ingestion.

## Files

| File | Contents |
|---|---|
| [`ingestion_throughput_summary.csv`](ingestion_throughput_summary.csv) | One row per workers value (ingested, failed, elapsed, books/s, CPU avg, WS max). |
| [`ingestion_resources_w1.csv`](ingestion_resources_w1.csv) … [`_w8.csv`](ingestion_resources_w8.csv) | 1 Hz CPU/RAM samples of the ingestion process during each run. |
| [`ingestion_throughput_run.log`](ingestion_throughput_run.log) | Raw transcript of the workers=8 run (2xx-only counting, status breakdown). |

Raw per-run transcripts and service logs for every workers value are under
`test/load/results/` (not committed; regenerate with the script below).

## Reproducing

```bash
go build -o bin/ingestion.exe ./cmd/ingestion   # optional; the script builds its own
powershell -File test/load/ingestion_throughput.ps1
# extend the corpus toward ~100 real IDs for the full audit run:
powershell -File test/load/ingestion_throughput.ps1 -Books "5,11,26,...,<more real IDs>"
```

The script writes per-run transcripts, resource CSVs and the summary CSV into
`test/load/results/` with a UTC timestamp, and prints the table above.
