# Architecture Diagrams

This document contains architecture diagrams for the Boogle Engine Stage 2 system. These diagrams illustrate the service boundaries, data flow, concurrency model, and storage architecture.

## System Overview

```mermaid
graph TB
    subgraph "Front-End Boundary"
        Browser[Web Browser]
    end
    
    subgraph "Service Layer"
        Search[Search Service<br/>:7003<br/>1 replica, stateless]
        Ingestion[Ingestion Service<br/>:7001<br/>1 replica, stateless]
        Indexing[Indexing Service<br/>:7002<br/>1 replica, stateful]
        Control[Control Module<br/>CLI tool<br/>on-demand]
    end
    
    subgraph "Storage Layer"
        Datalake[datalake/<br/>Filesystem<br/>Raw books]
        Datamart[(MongoDB<br/>:27017<br/>Indexed data)]
        ControlState[control/<br/>Filesystem<br/>Pipeline state]
    end
    
    subgraph "External"
        Gutenberg[Project Gutenberg<br/>HTTP API]
    end
    
    Browser -->|GET /search<br/>GET /books/:id| Search
    Search -->|Read| Datamart
    
    Control -->|POST /ingest/:id| Ingestion
    Control -->|POST /index/update/:id| Indexing
    Control -->|Read/Write| ControlState
    
    Ingestion -->|HTTP GET| Gutenberg
    Ingestion -->|Write raw files| Datalake
    
    Indexing -->|Read raw files| Datalake
    Indexing -->|Write index| Datamart
    
    style Search fill:#e1f5ff
    style Ingestion fill:#fff4e1
    style Indexing fill:#ffe1f5
    style Control fill:#f0f0f0
```

## Data Flow: Ingestion to Search

```mermaid
sequenceDiagram
    participant User
    participant Control
    participant Ingestion
    participant Gutenberg
    participant Datalake
    participant Indexing
    participant MongoDB
    
    User->>Control: Run with -ids 1342,84
    Control->>Control: Load state from control/
    
    Note over Control: For each book ID
    Control->>Ingestion: POST /ingest/1342
    Ingestion->>Gutenberg: HTTP GET book metadata
    Gutenberg-->>Ingestion: Headers + content
    Ingestion->>Datalake: Write 1342/header.txt<br/>Write 1342/body.txt
    Ingestion-->>Control: 200 OK {status: "available"}
    Control->>Control: Record in indexed_books.txt
    
    Control->>Indexing: POST /index/update/1342
    Indexing->>Datalake: Read 1342/header.txt<br/>Read 1342/body.txt
    Indexing->>Indexing: Tokenize text<br/>Build postings
    Indexing->>MongoDB: Upsert book metadata<br/>Upsert term postings
    Indexing-->>Control: 200 OK {status: "updated"}
    Control->>Control: Record in indexed_books.txt
    
    Note over Control: Repeat for book 84
    
    User->>Search: GET /search?q=adventure
    Search->>MongoDB: Query inverted_index<br/>Filter by metadata
    MongoDB-->>Search: Matching books
    Search-->>User: 200 OK {results: [...]}
```

## Concurrency Model: Control Module Workers

```mermaid
graph LR
    subgraph "Control Module Process"
        Scheduler[Scheduler<br/>reads candidates]
        
        subgraph "Worker Pool (default: 4)"
            W1[Worker 1<br/>goroutine]
            W2[Worker 2<br/>goroutine]
            W3[Worker 3<br/>goroutine]
            W4[Worker 4<br/>goroutine]
        end
        
        Queue[Candidate Queue<br/>thread-safe]
    end
    
    subgraph "External Services"
        Ingestion[Ingestion Service]
        Indexing[Indexing Service]
    end
    
    subgraph "Storage"
        State[control/<br/>flock-protected]
    end
    
    Scheduler --> Queue
    Queue --> W1
    Queue --> W2
    Queue --> W3
    Queue --> W4
    
    W1 -->|POST /ingest/:id| Ingestion
    W1 -->|POST /index/update/:id| Indexing
    W1 -->|Append| State
    
    W2 -->|POST /ingest/:id| Ingestion
    W2 -->|POST /index/update/:id| Indexing
    W2 -->|Append| State
    
    W3 -->|POST /ingest/:id| Ingestion
    W3 -->|POST /index/update/:id| Indexing
    W3 -->|Append| State
    
    W4 -->|POST /ingest/:id| Ingestion
    W4 -->|POST /index/update/:id| Indexing
    W4 -->|Append| State
    
    style W1 fill:#e1f5ff
    style W2 fill:#e1f5ff
    style W3 fill:#e1f5ff
    style W4 fill:#e1f5ff
```

## Storage Architecture

```mermaid
graph TB
    subgraph "Storage Layer (Single Host)"
        subgraph "Filesystem"
            subgraph "datalake/"
                direction TB
                B1[1342/<br/>header.txt<br/>body.txt]
                B2[84/<br/>header.txt<br/>body.txt]
                B3[.../<br/>...]
            end
            
            subgraph "control/"
                direction TB
                Downloaded[downloaded_books.txt<br/>append-only, flock]
                Indexed[indexed_books.txt<br/>append-only, flock]
                Failed[failed_books.txt<br/>append-only, flock]
            end
        end
        
        subgraph "MongoDB (boogle database)"
            direction TB
            Books[books collection<br/>_id: book_id<br/>Metadata]
            InvertedIndex[inverted_index collection<br/>_id: term<br/>Postings array]
        end
    end
    
    Ingestion -->|Atomic write:<br/>temp + rename| B1
    Ingestion -->|Atomic write:<br/>temp + rename| B2
    
    Indexing -->|Read| B1
    Indexing -->|Read| B2
    Indexing -->|Upsert| Books
    Indexing -->|Upsert| InvertedIndex
    
    Control -->|Append with flock| Downloaded
    Control -->|Append with flock| Indexed
    Control -->|Append with flock| Failed
    
    Search -->|Read| Books
    Search -->|Read| InvertedIndex
    
    style Datalake fill:#fff4e1
    style ControlState fill:#f0f0f0
    style Datamart fill:#ffe1f5
```

## Service Interaction and Thread Boundaries

```mermaid
graph TB
    subgraph "Host Machine (Single Machine)"
        subgraph "Ingestion Service Process"
            direction TB
            I_Server[HTTP Server<br/>Goroutine per request]
            I_Pool[Download Pool<br/>Semaphore: 8]
            I_FS[Filesystem Writer<br/>Atomic rename]
        end
        
        subgraph "Indexing Service Process"
            direction TB
            Idx_Server[HTTP Server<br/>Goroutine per request]
            Idx_Worker[Indexing Worker<br/>1 goroutine per request]
            Idx_Rebuild[Rebuild Worker<br/>1 goroutine, mutex]
        end
        
        subgraph "Search Service Process"
            direction TB
            S_Server[HTTP Server<br/>Goroutine per request]
            S_Query[Query Handler<br/>1 goroutine per request]
        end
        
        subgraph "Control Module Process (On-Demand)"
            direction TB
            C_Scheduler[Scheduler]
            C_Workers[Worker Pool<br/>N goroutines]
            C_State[State Manager<br/>flock-protected]
        end
        
        subgraph "MongoDB Process"
            direction TB
            Mongo_Server[MongoDB Server]
            Mongo_Pool[Connection Pool<br/>maxPoolSize: 100 per client]
        end
    end
    
    C_Workers -->|HTTP POST| I_Server
    C_Workers -->|HTTP POST| Idx_Server
    S_Server -->|MongoDB queries| Mongo_Pool
    
    I_Pool -->|Download| I_FS
    Idx_Worker -->|Read files| I_FS
    Idx_Worker -->|Write index| Mongo_Pool
    Idx_Rebuild -->|Rebuild index| Mongo_Pool
    
    style I_Server fill:#fff4e1
    style Idx_Server fill:#ffe1f5
    style S_Server fill:#e1f5ff
    style C_Workers fill:#f0f0f0
```

## Stage 3 Migration: What Changes

```mermaid
graph LR
    subgraph "Stage 2 (Current)"
        direction TB
        S2_Ingestion[Ingestion<br/>1 replica]
        S2_Indexing[Indexing<br/>1 replica]
        S2_Search[Search<br/>1 replica]
        S2_Mongo[MongoDB<br/>1 instance]
        S2_FS[Shared Filesystem<br/>datalake/, control/]
    end
    
    subgraph "Stage 3 (Future)"
        direction TB
        S3_LB[Load Balancer<br/>nginx / HAProxy]
        S3_Ingestion[Ingestion<br/>2-4 replicas]
        S3_Indexing[Indexing<br/>1 replica<br/>+ distributed lock]
        S3_Search[Search<br/>2-4 replicas]
        S3_Mongo[(MongoDB<br/>Replica Set<br/>3 nodes)]
        S3_Object[Object Storage<br/>S3 / MinIO]
        S3_DB[(Database<br/>PostgreSQL / Redis<br/>Control state)]
    end
    
    S2_Ingestion -.->|Scale horizontally| S3_Ingestion
    S2_Indexing -.->|Add distributed lock| S3_Indexing
    S2_Search -.->|Scale horizontally| S3_Search
    S2_Mongo -.->|Replica set| S3_Mongo
    S2_FS -.->|Move to object storage + DB| S3_Object
    S2_FS -.->|Move to object storage + DB| S3_DB
    
    S3_LB --> S3_Ingestion
    S3_LB --> S3_Search
    
    style S2_Ingestion fill:#fff4e1
    style S2_Indexing fill:#ffe1f5
    style S2_Search fill:#e1f5ff
    style S3_Ingestion fill:#fff4e1
    style S3_Indexing fill:#ffe1f5
    style S3_Search fill:#e1f5ff
```

## Key Architectural Decisions

### 1. Classical HTTP Orchestration (No Message Broker)

**Decision:** Control Module coordinates services via direct HTTP calls.

**Rationale:**
- Simpler deployment (no Kafka/RabbitMQ infrastructure)
- Easier debugging (synchronous request-response)
- Adequate for single-host Stage 2

**Trade-off:**
- Control Module is a single point of failure
- Cannot scale control horizontally without distributed coordination

### 2. Filesystem-Based Datalake and Control State

**Decision:** Use local filesystem with atomic writes (temp file + rename) and flock.

**Rationale:**
- Simple, no external dependencies
- Fast for single-host deployment
- Crash-safe with atomic operations

**Trade-off:**
- Cannot share across multiple hosts in Stage 3
- Must migrate to object storage (S3) and database

### 3. Single-Replica Indexing with In-Process Lock

**Decision:** Indexing service runs as single replica, rebuild serialized with mutex.

**Rationale:**
- Simpler implementation
- No distributed lock needed for Stage 2

**Trade-off:**
- Cannot scale indexing horizontally
- Must add distributed lock (MongoDB document, Redis, etcd) for Stage 3

### 4. MongoDB as Datamart

**Decision:** Use MongoDB for inverted index and metadata storage.

**Rationale:**
- Flexible schema for book metadata
- Good support for array fields (postings)
- Driver available for Go

**Trade-off:**
- Posting arrays may hit 16MB document limit at scale
- Full array rewrite on each update (expensive for large postings)
- Stage 3: consider sharding or alternative data model (one doc per term-book pair)

### 5. Worker Pool in Control Module

**Decision:** Configurable worker pool (default: 4) with goroutines.

**Rationale:**
- Concurrency without complexity of async programming
- Easy to tune via `-workers` flag

**Trade-off:**
- Optimal value depends on workload (benchmark shows 8 is best)
- Beyond optimal, contention reduces gains

## Thread Safety Summary

| Component | Mechanism | Scope | Stage 3 Impact |
|-----------|-----------|-------|----------------|
| Control state files | flock (Unix) / LockFileEx (Windows) | Cross-process | Must move to database |
| Datalake writes | Atomic rename (temp + final) | Cross-process | Must move to object storage |
| MongoDB writes | Driver connection pool + transactions | Cross-process | Scale with replica set |
| Indexing rebuild | In-process mutex | Single process | Must add distributed lock |
| Ingestion downloads | Semaphore (counting) | Single process | Scale with replicas |
