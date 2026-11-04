package indexing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

const statsTimeout = 5 * time.Second

var ErrMissingPort = errors.New("indexing service needs a book source, an index writer, a rebuild locker and index metrics")

type Service struct {
	ports        Ports
	pendingMutex sync.Mutex
	pending      []models.BookID
	rebuilding   atomic.Bool
	lastUpdate   atomic.Int64
}

func NewService(ports Ports) (*Service, error) {
	if ports.Source == nil || ports.Writer == nil || ports.Locker == nil || ports.Metrics == nil {
		return nil, ErrMissingPort
	}
	service := &Service{ports: ports, pending: []models.BookID{}}
	service.markUpdated()
	return service, nil
}

func (service *Service) IndexBook(ctx context.Context, bookID models.BookID) error {
	input, err := service.prepare(bookID)
	if err != nil {
		return err
	}
	// The flag must be read before the write, so a swap can never overtake a book that needs replaying.
	if service.rebuilding.Load() {
		service.recordPending(bookID)
	}
	if err := service.ports.Writer.IndexBook(ctx, input); err != nil {
		return fmt.Errorf("write book %d to the index: %w", bookID, err)
	}
	service.markUpdated()
	return nil
}

func (service *Service) Rebuild(ctx context.Context) (indexer.RebuildReport, error) {
	lease, err := service.ports.Locker.AcquireRebuildLease(ctx)
	if errors.Is(err, indexer.ErrRebuildInProgress) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	if err != nil {
		return indexer.RebuildReport{}, fmt.Errorf("claim rebuild lease: %w", err)
	}
	defer releaseLease(lease)
	if !service.rebuilding.CompareAndSwap(false, true) {
		return indexer.RebuildReport{}, indexer.ErrRebuildInProgress
	}
	startedAt := time.Now()
	processed, err := service.buildFreshIndex(ctx)
	pending := service.finishRebuild()
	if err != nil {
		return indexer.RebuildReport{}, err
	}
	if err := service.replay(ctx, pending); err != nil {
		return indexer.RebuildReport{}, err
	}
	service.markUpdated()
	return indexer.RebuildReport{BooksProcessed: processed, Elapsed: time.Since(startedAt)}, nil
}

func (service *Service) Stats() indexer.IndexStats {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	return indexer.IndexStats{
		BooksIndexed: service.publishedBooks(ctx),
		LastUpdate:   time.Unix(0, service.lastUpdate.Load()).UTC(),
		SizeBytes:    service.dataSize(ctx),
	}
}

func (service *Service) prepare(bookID models.BookID) (models.IndexBookInput, error) {
	book, err := service.ports.Source.ReadBook(bookID)
	if err != nil {
		return models.IndexBookInput{}, fmt.Errorf("read book %d: %w", bookID, err)
	}
	return models.IndexBookInput{
		Metadata: indexer.ExtractMetadata(book.ID, book.Header),
		Tokens:   indexer.ProcessText(book.Body),
	}, nil
}

func (service *Service) buildFreshIndex(ctx context.Context) (int, error) {
	bookIDs, err := service.ports.Source.ListBookIDs()
	if err != nil {
		return 0, fmt.Errorf("list source books: %w", err)
	}
	processed, err := service.ports.Writer.Rebuild(ctx, service.nextBook(bookIDs))
	if err != nil {
		return 0, fmt.Errorf("rebuild index: %w", err)
	}
	return processed, nil
}

func (service *Service) nextBook(bookIDs []models.BookID) NextBook {
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
		input, err := service.prepare(bookID)
		if err != nil {
			return models.IndexBookInput{}, false, err
		}
		return input, true, nil
	}
}

func (service *Service) finishRebuild() []models.BookID {
	service.rebuilding.Store(false)
	service.pendingMutex.Lock()
	defer service.pendingMutex.Unlock()
	pending := service.pending
	service.pending = []models.BookID{}
	return pending
}

func (service *Service) recordPending(bookID models.BookID) {
	service.pendingMutex.Lock()
	defer service.pendingMutex.Unlock()
	service.pending = append(service.pending, bookID)
}

func (service *Service) replay(ctx context.Context, pending []models.BookID) error {
	for _, bookID := range pending {
		if err := service.IndexBook(ctx, bookID); err != nil {
			return fmt.Errorf("replay book %d after rebuild: %w", bookID, err)
		}
	}
	return nil
}

func (service *Service) publishedBooks(ctx context.Context) int {
	count, err := service.ports.Metrics.CountPublishedBooks(ctx)
	if err != nil {
		slog.Warn("[INDEXING] count published books", "error", err)
		return 0
	}
	return count
}

func (service *Service) dataSize(ctx context.Context) uint64 {
	size, err := service.ports.Metrics.DataSizeBytes(ctx)
	if err != nil {
		slog.Warn("[INDEXING] measure index size", "error", err)
		return 0
	}
	return size
}

func (service *Service) markUpdated() {
	service.lastUpdate.Store(time.Now().UnixNano())
}

func releaseLease(lease RebuildLease) {
	if err := lease.Release(); err != nil {
		slog.Error("[INDEXING] release rebuild lease", "error", err)
	}
}
