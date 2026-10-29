# Replicas and Threads Configuration

This document describes the replica count, thread settings, and concurrency assumptions for each service in Stage 2. These configurations will need to change in Stage 3 when moving to a distributed deployment.

## Service Replicas

| Service | Replicas (Stage 2) | Replicas (Stage 3) | Notes |
|---------|-------------------|-------------------|-------|
| Ingestion | 1 | 2-4 | Stateless, can scale horizontally. Load balancer needed. |
| Indexing | 1 | 1 | Rebuild guard is per-process. Multiple replicas would race. |
| Search | 1 | 2-4 | Stateless, can scale horizontally. Load balancer needed. |
| Control | 1 (on-demand) | 1 | State lives in memory. Multiple instances would duplicate work. |
| MongoDB | 1 | Replica set | Use MongoDB replica set for HA, not multiple standalone instances. |

## Thread and Concurrency Settings

### Control Module

| Setting | Value | Description |
|---------|-------|-------------|
| `-workers` | 4 (default) | Concurrent pipeline workers. Each worker processes one book at a time. |
| GOMAXPROCS | Runtime default | Go runtime sets this to number of CPU cores. Can override with `GOMAXPROCS=N`. |
| Goroutines per request | 1 | Each worker is a goroutine. HTTP client calls are synchronous within a worker. |
| Max concurrent books | 4 | Limited by `-workers` flag. |

**Tuning guidance:**
- Increase `-workers` to improve throughput (see benchmark: 1→8 workers improves from 0.44 to 1.10 books/s)
- Beyond 8 workers, index step contention may reduce gains
- Monitor `books_per_second`, `ingest_step_avg`, `index_step_avg` to find optimal value

### Ingestion Service

| Setting | Value | Description |
|---------|-------|-------------|
| `-workers` | 8 (default) | Max concurrent Gutenberg downloads |
| `-ingest-timeout` | 2m | Timeout per ingest request |
| Server read timeout | 5s | HTTP server read timeout |
| Server write timeout | 10s | HTTP server write timeout |
| Goroutines per request | 1 | Each HTTP request handled by one goroutine |

**Tuning guidance:**
- Increase `-workers` if ingestion is the bottleneck (check `ingest_step_avg`)
- Monitor Gutenberg API rate limits before increasing beyond 16

### Indexing Service

| Setting | Value | Description |
|---------|-------|-------------|
| Server read timeout | 5s | HTTP server read timeout |
| Server write timeout | 30s | HTTP server write timeout (indexing can be slow) |
| Goroutines per request | 1 | Each HTTP request handled by one goroutine |
| Rebuild concurrency | 1 (single goroutine) | Rebuild is serialized via mutex |

**Tuning guidance:**
- Indexing is CPU-bound (tokenization, posting list construction)
- Multiple replicas not supported (rebuild guard is per-process)
- If indexing is the bottleneck, scale vertically (more CPU) not horizontally

### Search Service

| Setting | Value | Description |
|---------|-------|-------------|
| Server read timeout | 5s | HTTP server read timeout |
| Server write timeout | 10s | HTTP server write timeout |
| Goroutines per request | 1 | Each HTTP request handled by one goroutine |

**Tuning guidance:**
- Search is I/O-bound (MongoDB reads)
- Stateless, can scale horizontally with load balancer
- Monitor MongoDB connection pool utilization

### MongoDB

| Setting | Value | Description |
|---------|-------|-------------|
| Connection pool size | 100 | Default MongoDB driver pool size |
| Max connections per service | 100 | Each service maintains its own pool |
| Total connections (Stage 2) | 300 | 3 services × 100 connections |

**Tuning guidance:**
- If connection pool exhaustion occurs, increase `maxPoolSize` in connection URI
- Monitor MongoDB `currentOps` and `connectionStats`
- Consider connection pooling middleware for Stage 3

## Stage 2 → Stage 3 Migration: What Breaks

### 1. Indexing Service Single Replica Assumption

**Current (Stage 2):** Indexing runs as a single replica. Rebuild uses an in-process mutex to serialize rebuilds.

**Breaks in Stage 3:** Multiple indexing replicas would each have their own mutex, allowing concurrent rebuilds that race and corrupt the index.

**Fix for Stage 3:** Implement distributed lock (MongoDB document with TTL, or Redis/etcd lock) so only one replica can rebuild at a time.

### 2. Control Module State in Memory

**Current (Stage 2):** Control module loads state into memory at startup. Multiple instances would each have their own copy and duplicate work.

**Breaks in Stage 3:** Multiple control instances would process the same books twice, wasting resources and potentially causing conflicts.

**Fix for Stage 3:** Either:
- Keep control as a single instance (acceptable for batch processing)
- Move state to shared storage (Redis, database) and implement distributed coordination

### 3. Service Discovery via Static URLs

**Current (Stage 2):** Services connect via static URLs (`http://ingestion:7001`, etc.). Assumes single replica of each service.

**Breaks in Stage 3:** With multiple replicas, need load balancing and service discovery.

**Fix for Stage 3:** Use:
- DNS-based load balancing (Kubernetes Service, Consul)
- Or explicit load balancer (nginx, HAProxy) in front of each service
- Or service mesh (Istio, Linkerd) for advanced routing

### 4. Shared Filesystem Assumption

**Current (Stage 2):** All services share the same filesystem (datalake, control state). Assumes single host.

**Breaks in Stage 3:** Multiple hosts cannot share local filesystem.

**Fix for Stage 3:** Use:
- Distributed filesystem (NFS, EFS)
- Object storage (S3, MinIO) for datalake
- Database for control state (PostgreSQL, Redis)

### 5. MongoDB Single Instance

**Current (Stage 2):** Single MongoDB instance. No replication.

**Breaks in Stage 3:** Single point of failure. No high availability.

**Fix for Stage 3:** Deploy MongoDB as a replica set (3 nodes minimum for HA). Update connection URIs to use replica set configuration.

### 6. Health Checks Assume Single Instance

**Current (Stage 2):** Health endpoints check local service state.

**Breaks in Stage 3:** With multiple replicas, need to check:
- Service is alive (liveness)
- Service is ready to accept traffic (readiness)
- Service can reach dependencies (MongoDB, other services)

**Fix for Stage 3:** Enhance health endpoints to:
- Check MongoDB connectivity
- Check datalake accessibility
- Report replica set status (for MongoDB)

## Concurrency Safety Summary

| Component | Thread-Safe? | Cross-Process Safe? | Notes |
|-----------|--------------|---------------------|-------|
| Control state files | ✅ (flock) | ✅ (flock) | Atomic appends with file locking |
| Datalake writes | ✅ (atomic rename) | ✅ (atomic rename) | Write to temp, rename to final |
| MongoDB writes | ✅ (driver) | ✅ (transactions) | Idempotent upserts |
| Indexing rebuild | ✅ (mutex) | ❌ (per-process) | Needs distributed lock in Stage 3 |
| Ingestion downloads | ✅ (semaphore) | ✅ (idempotent) | Rate-limited via semaphore |

## Performance Baselines (Stage 2)

From `coordination_workers_*.txt` benchmarks:

- **Optimal workers:** 8 (1.10 books/s, 0 failures)
- **Ingestion throughput:** ~430-590ms per book (ingest_step_avg)
- **Indexing throughput:** 1.4-6.6s per book (index_step_avg, increases with workers due to MongoDB load)
- **End-to-end throughput:** 0.44-1.10 books/s depending on worker count

These baselines should be re-measured in Stage 3 after implementing distributed coordination.
