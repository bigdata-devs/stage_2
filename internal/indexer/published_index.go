package indexer

import (
	"context"
	"errors"
	"fmt"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"time"

	"boogle-engine/internal/models"
)

// liveHeapMetric is the heap still in use after the last garbage collection (Go 1.21+).
const liveHeapMetric = "/gc/heap/live:bytes"

// ErrRebuildInProgress is returned by Rebuild while another rebuild is still running.
var ErrRebuildInProgress = errors.New("an index rebuild is already running")

// BookSource is the read side of the datalake that a published index is built from.
// datalake.Datalake implements it.
type BookSource interface {
	BookReader
	ListBookIDs() ([]models.BookID, error)
}

// IndexStats summarizes the published index.
type IndexStats struct {
	BooksIndexed int
	LastUpdate   time.Time
	SizeBytes    uint64
}

// RebuildReport describes a rebuild that has been published.
type RebuildReport struct {
	BooksProcessed int
	Elapsed        time.Duration
}

// generation is one complete set of index structures plus the Indexer that fills them.
type generation struct {
	catalog *MetadataCatalog
	index   *InvertedIndex
	indexer Indexer
}

func newGeneration(reader BookReader) generation {
	catalog, index := NewMetadataCatalog(), NewInvertedIndex()
	return generation{catalog: catalog, index: index, indexer: NewIndexer(reader, catalog, index)}
}

// PublishedIndex owns the index that the Indexing Service publishes and keeps it consistent:
//
//   - IndexBook adds one book to the published generation; re-indexing a book changes nothing.
//   - Rebuild builds a brand-new generation from every book in the datalake while the current
//     one keeps answering, then publishes it in a single swap. A failed or interrupted rebuild
//     is discarded and the previous generation stays published, never a half-built one.
//   - While a rebuild runs, IndexBook writes to both generations, so a rebuild never loses an
//     update and never blocks one.
//
// It is safe for concurrent use. Its locks coordinate the goroutines of one process only.
type PublishedIndex struct {
	source     BookSource
	workers    int
	mutex      sync.RWMutex // held for reading by IndexBook, for writing to change generations
	live       generation
	building   []generation // the generation under construction during a rebuild, otherwise empty
	rebuilding atomic.Bool
	lastUpdate atomic.Int64 // Unix nanoseconds of the last change to the published index
}

// NewPublishedIndex publishes an empty index over source. workers is the number of goroutines
// that index books in parallel during a rebuild.
func NewPublishedIndex(source BookSource, workers int) (*PublishedIndex, error) {
	if source == nil {
		return &PublishedIndex{}, errors.New("published index needs a book source")
	}
	if workers < 1 {
		return &PublishedIndex{}, fmt.Errorf("rebuild workers must be a positive integer, got %d", workers)
	}
	published := &PublishedIndex{source: source, workers: workers, live: newGeneration(source)}
	published.markUpdated()
	return published, nil
}

// IndexBook reads one book from the datalake and adds it to the published index and, during a
// rebuild, to the generation being built as well.
func (published *PublishedIndex) IndexBook(bookID models.BookID) error {
	published.mutex.RLock()
	defer published.mutex.RUnlock()
	for _, target := range append([]generation{published.live}, published.building...) {
		if err := target.indexer.IndexBook(bookID); err != nil {
			return err
		}
	}
	published.markUpdated()
	return nil
}

// Rebuild replaces the published index with one built from scratch out of every complete book
// in the datalake. It returns ErrRebuildInProgress if another rebuild is running, and stops
// early, keeping the previous index, when ctx ends or a book cannot be indexed.
func (published *PublishedIndex) Rebuild(ctx context.Context) (RebuildReport, error) {
	if !published.rebuilding.CompareAndSwap(false, true) {
		return RebuildReport{}, ErrRebuildInProgress
	}
	defer published.rebuilding.Store(false)
	startedAt := time.Now()
	next := published.startBuilding()
	booksProcessed, err := published.fill(ctx, next)
	if err != nil {
		published.abandonBuilding()
		return RebuildReport{}, fmt.Errorf("rebuild index: %w", err)
	}
	published.publish(next)
	return RebuildReport{BooksProcessed: booksProcessed, Elapsed: time.Since(startedAt)}, nil
}

// Stats describes the published generation; a rebuild in progress is not reflected until it
// is published. SizeBytes is a rough estimate: the live heap of the whole process, which the
// in-memory index dominates (during a rebuild it holds both generations).
func (published *PublishedIndex) Stats() IndexStats {
	published.mutex.RLock()
	defer published.mutex.RUnlock()
	return IndexStats{
		BooksIndexed: published.live.catalog.Count(),
		LastUpdate:   time.Unix(0, published.lastUpdate.Load()).UTC(),
		SizeBytes:    liveHeapBytes(),
	}
}

// startBuilding registers a new, empty generation so that every IndexBook from now on also
// reaches it. The datalake is listed only afterwards, so no book can slip between the two.
func (published *PublishedIndex) startBuilding() generation {
	next := newGeneration(published.source)
	published.mutex.Lock()
	defer published.mutex.Unlock()
	published.building = []generation{next}
	return next
}

// fill indexes every complete book of the datalake into next and returns how many there were.
func (published *PublishedIndex) fill(ctx context.Context, next generation) (int, error) {
	bookIDs, err := published.source.ListBookIDs()
	if err != nil {
		return 0, fmt.Errorf("list datalake books: %w", err)
	}
	if err := indexInParallel(ctx, next.indexer, bookIDs, published.workers); err != nil {
		return 0, err
	}
	return len(bookIDs), nil
}

// publish swaps next in as the published generation in one step.
func (published *PublishedIndex) publish(next generation) {
	published.mutex.Lock()
	defer published.mutex.Unlock()
	published.live = next
	published.building = []generation{}
	published.markUpdated()
}

func (published *PublishedIndex) abandonBuilding() {
	published.mutex.Lock()
	defer published.mutex.Unlock()
	published.building = []generation{}
}

func (published *PublishedIndex) markUpdated() {
	published.lastUpdate.Store(time.Now().UnixNano())
}

// indexInParallel indexes the books with a fixed number of worker goroutines. It stops handing
// out books at the first failure or when ctx ends, and returns that cause.
func indexInParallel(ctx context.Context, indexer Indexer, bookIDs []models.BookID, workers int) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := feedBookIDs(ctx, bookIDs)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			indexJobs(indexer, jobs, cancel)
		}()
	}
	waitGroup.Wait()
	return context.Cause(ctx)
}

// feedBookIDs sends the IDs one by one and closes the channel when done or when ctx ends.
func feedBookIDs(ctx context.Context, bookIDs []models.BookID) <-chan models.BookID {
	jobs := make(chan models.BookID)
	go func() {
		defer close(jobs)
		for _, bookID := range bookIDs {
			select {
			case jobs <- bookID:
			case <-ctx.Done():
				return
			}
		}
	}()
	return jobs
}

// indexJobs is the loop of one rebuild worker; a failure cancels the whole rebuild.
func indexJobs(indexer Indexer, jobs <-chan models.BookID, cancel context.CancelCauseFunc) {
	for bookID := range jobs {
		if err := indexer.IndexBook(bookID); err != nil {
			cancel(err)
		}
	}
}

// liveHeapBytes reads the live heap without stopping the world; it is 0 before the first GC.
func liveHeapBytes() uint64 {
	samples := []metrics.Sample{{Name: liveHeapMetric}}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return samples[0].Value.Uint64()
}
