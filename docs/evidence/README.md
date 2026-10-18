# Stage 2 Evidence — MongoDB Datamart (Member 4)

Captured on 2026-10-18 from branch `feature/mongodb-datamart` (HEAD `6cc1e85`).

| File | Contents |
|---|---|
| [`tests.txt`](tests.txt) | Verbose run of the full `internal/mongodb` suite: **34 PASS** (30 tests + 4 subtests), **0 skipped, 0 failed**, `go test -v -count=1 ./internal/mongodb/` against the live `mongo:7` container. |
| [`benchmarks.txt`](benchmarks.txt) | Raw `go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/` output: four benchmarks × ten samples (300-book corpus, one second or more per sample). |
| [`benchstat.txt`](benchstat.txt) | The same run aggregated with `benchstat` (medians with 95 % confidence intervals, plus `B/op` and `allocs/op`). |
| [`demo.md`](demo.md) | End-to-end transcript: datalake → Indexing Service → datamart → Search Service, with the literal request/response bodies of §1–§4. |

## Environment

- Windows 11, Go `go1.27.0` (module targets `go1.25.0`), `goarch=amd64`
- MongoDB `7.0.45` in Docker (`boogle-mongo-test`, port `27017`, database `boogle`)
- CPU: Intel Core i7-14700HX (benchmarks report `GOMAXPROCS=28`)

## Reproducing

```bash
docker start boogle-mongo-test                              # live tests skip without a server
gofmt -l . && go vet ./... && go test ./...                 # repository verification

go test -v -count=1 ./internal/mongodb/ > docs/evidence/tests.txt   # §tests
go test -run '^$' -bench . -benchmem -benchtime 1s -count 10 ./internal/mongodb/   # §benchmarks
go run golang.org/x/perf/cmd/benchstat@latest <benchmarks-output>                  # §benchstat
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
