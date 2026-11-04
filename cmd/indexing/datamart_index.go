package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

const statsTimeout = 5 * time.Second

type DatamartIndex struct {
	source       indexer.BookSource
	writer       mongodb.BookIndexer
	client       mongodb.Client
	pendingMutex sync.Mutex
	pending      []models.BookID
	rebuilding   atomic.Bool
	lastUpdate   atomic.Int64
}

func NewDatamartIndex(source indexer.BookSource, client mongodb.Client) *DatamartIndex {
	adapted := &DatamartIndex{
		source:  source,
		writer:  mongodb.NewBookIndexer(client),
		client:  client,
		pending: []models.BookID{},
	}
	adapted.markUpdated()
	return adapted
}

func (index *DatamartIndex) IndexBook(ctx context.Context, bookID models.BookID) error {
	book, err := index.source.ReadBook(bookID)
	if err != nil {
		return fmt.Errorf("read book %d: %w", bookID, err)
	}
	input := models.IndexBookInput{
		Metadata: indexer.ExtractMetadata(book.ID, book.Header),
		Tokens:   indexer.ProcessText(book.Body),
	}
	if index.rebuilding.Load() {
		index.recordPending(bookID)
	}
	if err := index.writer.IndexBook(ctx, input); err != nil {
		return fmt.Errorf("index book %d in datamart: %w", bookID, err)
	}
	index.markUpdated()
	return nil
}

func (index *DatamartIndex) Rebuild(ctx context.Context) (indexer.RebuildReport, error) {
	lock, err := index.client.AcquireRebuildLock(ctx)
	if errors.Is(err, mongodb.ErrRebuildLocked) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	if err != nil {
		return indexer.RebuildReport{}, fmt.Errorf("claim rebuild lock: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			slog.Error("[INDEXING] release rebuild lock", "error", releaseErr)
		}
	}()
	if !index.rebuilding.CompareAndSwap(false, true) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	startedAt := time.Now()
	processed, err := index.buildFreshDatamart(ctx)
	pending := index.finishRebuild()
	if err != nil {
		return indexer.RebuildReport{}, err
	}
	if err := index.replayPending(ctx, pending); err != nil {
		return indexer.RebuildReport{}, err
	}
	index.markUpdated()
	return indexer.RebuildReport{BooksProcessed: processed, Elapsed: time.Since(startedAt)}, nil
}

func (index *DatamartIndex) Stats() indexer.IndexStats {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	return indexer.IndexStats{
		BooksIndexed: index.countPublishedBooks(ctx),
		LastUpdate:   time.Unix(0, index.lastUpdate.Load()).UTC(),
		SizeBytes:    index.datamartBytes(ctx),
	}
}

func (index *DatamartIndex) buildFreshDatamart(ctx context.Context) (int, error) {
	bookIDs, err := index.source.ListBookIDs()
	if err != nil {
		return 0, fmt.Errorf("list datalake books: %w", err)
	}
	result, err := index.writer.Rebuild(ctx, index.nextBook(bookIDs))
	if err != nil {
		return 0, fmt.Errorf("rebuild datamart: %w", err)
	}
	return result.BooksProcessed, nil
}

func (index *DatamartIndex) nextBook(bookIDs []models.BookID) mongodb.NextBook {
	position := 0
	return func(ctx context.Context) (models.IndexBookInput, bool, error) {
		if err := ctx.Err(); err != nil {
			return models.IndexBookInput{}, false, err
		}
		if position >= len(bookIDs) {
			return models.IndexBookInput{}, false, nil
		}
		bookID := bookIDs[position]
		position++
		book, err := index.source.ReadBook(bookID)
		if err != nil {
			return models.IndexBookInput{}, false, fmt.Errorf("read book %d: %w", bookID, err)
		}
		return models.IndexBookInput{
			Metadata: indexer.ExtractMetadata(book.ID, book.Header),
			Tokens:   indexer.ProcessText(book.Body),
		}, true, nil
	}
}

func (index *DatamartIndex) finishRebuild() []models.BookID {
	index.rebuilding.Store(false)
	index.pendingMutex.Lock()
	defer index.pendingMutex.Unlock()
	pending := index.pending
	index.pending = []models.BookID{}
	return pending
}

func (index *DatamartIndex) recordPending(bookID models.BookID) {
	index.pendingMutex.Lock()
	defer index.pendingMutex.Unlock()
	index.pending = append(index.pending, bookID)
}

func (index *DatamartIndex) replayPending(ctx context.Context, pending []models.BookID) error {
	for _, bookID := range pending {
		if err := index.IndexBook(ctx, bookID); err != nil {
			return fmt.Errorf("replay book %d after rebuild: %w", bookID, err)
		}
	}
	return nil
}

func (index *DatamartIndex) countPublishedBooks(ctx context.Context) int {
	count, err := index.client.Database().Collection(mongodb.BooksCollection).
		CountDocuments(ctx, bson.D{{Key: "status", Value: mongodb.StatusPublished}})
	if err != nil {
		slog.Warn("[INDEXING] count published books", "error", err)
		return 0
	}
	return int(count)
}

func (index *DatamartIndex) datamartBytes(ctx context.Context) uint64 {
	var stats struct {
		DataSize int64 `bson:"dataSize"`
	}
	command := bson.D{{Key: "dbStats", Value: 1}}
	if err := index.client.Database().RunCommand(ctx, command).Decode(&stats); err != nil {
		slog.Warn("[INDEXING] measure datamart size", "error", err)
		return 0
	}
	return uint64(max(stats.DataSize, 0))
}

func (index *DatamartIndex) markUpdated() {
	index.lastUpdate.Store(time.Now().UnixNano())
}
