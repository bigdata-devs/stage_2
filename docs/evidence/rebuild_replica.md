# Rebuild Replica Experiment (audit RF 8 / benchmark protocol §6)

**2026-11-01**, host of the official session (see the host record in
[`benchmark_protocol.md`](benchmark_protocol.md)), commit `ade7245`, database `boogle_bench`,
datalake volume with **383 books**. Verifies that the cross-replica rebuild lease
(`internal/mongodb/rebuild_lock.go`) lets any number of Indexing replicas share one datamart
without two rebuilds racing.

## Setup

Two Indexing replicas of the same image, same MongoDB, same datalake volume, different host ports:

```bash
docker compose stop indexing                       # free port 7002

docker run -d --name boogle-indexing-a --network boogle-engine_default \
  -e MONGO_URI=mongodb://mongo:27017 -e MONGO_DATABASE=boogle_bench \
  -v boogle-engine_datalake:/data/datalake -p 7002:7002 \
  boogle-engine/indexing:latest -addr :7002 -datalake /data/datalake -rebuild-timeout 15m

docker run -d --name boogle-indexing-b --network boogle-engine_default \
  -e MONGO_URI=mongodb://mongo:27017 -e MONGO_DATABASE=boogle_bench \
  -v boogle-engine_datalake:/data/datalake -p 7012:7002 \
  boogle-engine/indexing:latest -addr :7002 -datalake /data/datalake -rebuild-timeout 15m
```

Both answer `GET /index/status` before the experiment starts:

```json
{"books_indexed":300,"last_update":"2026-11-01T16:39:23Z","index_size_MB":61.1}
```

## Concurrent rebuild — expected vs observed

Expected (protocol §6): one request answers `200` with the rebuild report, the overlapping one
answers `409`, and a retry after the first rebuild finishes succeeds.

```bash
curl -X POST http://localhost:7002/index/rebuild &   # replica A
sleep 2
curl -X POST http://localhost:7012/index/rebuild     # replica B, while A holds the lease
```

| # | Moment (UTC) | Request | Response | Body |
|---|---|---|---|---|
| 1 | 16:39:24 | `POST :7002/index/rebuild` (A) | starts, holds the lease | — |
| 2 | 16:39:24 | `POST :7012/index/rebuild` (B, +2 s) | **409** (immediate, <1 s) | `{"error":"rebuild_in_progress","message":"an index rebuild is already running"}` |
| 3 | 16:47:25 | A completes (**8 m 00.9 s**) | **200** | `{"books_processed":383,"elapsed_time":"8m0.945s"}` |
| 4 | 16:47:25 → 16:55:19 | retry `POST :7012/index/rebuild` (B) | **200** (7 m 54.5 s) | `{"books_processed":383,"elapsed_time":"7m54.507s"}` |

**All three expectations hold.** The lease is cluster-wide (a MongoDB document claimed with
`findOneAndUpdate`), so the second replica never starts a second rebuild; it fails fast with the
documented `409 rebuild_in_progress` instead of overwriting the `*_rebuild` collections. Once the
holder releases the lease, the retry succeeds and processes the whole datalake.

## Notes

- The rebuild is synchronous: the `200` arrives when all 383 books have been re-indexed, which is
  why both successful calls take about 8 minutes (one book≈ 1.3 s through the single-writer path).
- `POST /index/update` calls arriving during a rebuild are not blocked: they are recorded and
  replayed after the swap (see `docs/mongodb.md`), so the pipeline keeps running while the lease
  is held.
- Cleanup: `docker rm -f boogle-indexing-a boogle-indexing-b && docker compose start indexing`.
