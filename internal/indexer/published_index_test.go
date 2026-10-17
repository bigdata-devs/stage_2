package indexer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"boogle-engine/internal/models"
)

var errUnreadableBook = errors.New("unreadable book")

// fakeDatalake is a BookSource whose books can change during a test. Reading the book held by
// holdBookID signals reachedHold and then waits until releaseHold is closed, which lets a test
// pause a rebuild in the middle. The hold fields are set before any goroutine starts.
type fakeDatalake struct {
	mutex       sync.Mutex
	books       map[models.BookID]models.Book
	unreadable  map[models.BookID]bool
	holdBookID  models.BookID
	reachedHold chan struct{}
	releaseHold chan struct{}
	signalHold  sync.Once
}

func newFakeDatalake(bookIDs ...models.BookID) *fakeDatalake {
	lake := &fakeDatalake{books: map[models.BookID]models.Book{}, unreadable: map[models.BookID]bool{}}
	for _, bookID := range bookIDs {
		lake.add(bookID)
	}
	return lake
}

func (lake *fakeDatalake) add(bookID models.BookID) {
	lake.mutex.Lock()
	defer lake.mutex.Unlock()
	lake.books[bookID] = models.Book{
		ID:     bookID,
		Header: fmt.Sprintf("Title: Book %d\nLanguage: English", bookID),
		Body:   fmt.Sprintf("shared words plus marker%c", 'a'+rune(bookID%26)),
	}
}

func (lake *fakeDatalake) remove(bookID models.BookID) {
	lake.mutex.Lock()
	defer lake.mutex.Unlock()
	delete(lake.books, bookID)
}

// holdWhenReading makes the next rebuild pause when it reads bookID.
func (lake *fakeDatalake) holdWhenReading(bookID models.BookID) {
	lake.holdBookID = bookID
	lake.reachedHold = make(chan struct{})
	lake.releaseHold = make(chan struct{})
}

func (lake *fakeDatalake) markUnreadable(bookID models.BookID) {
	lake.mutex.Lock()
	defer lake.mutex.Unlock()
	lake.unreadable[bookID] = true
}

func (lake *fakeDatalake) ReadBook(bookID models.BookID) (models.Book, error) {
	if lake.releaseHold != nil && bookID == lake.holdBookID {
		lake.signalHold.Do(func() { close(lake.reachedHold) })
		<-lake.releaseHold
	}
	lake.mutex.Lock()
	defer lake.mutex.Unlock()
	book, found := lake.books[bookID]
	if !found || lake.unreadable[bookID] {
		return models.Book{}, fmt.Errorf("%w: %d", errUnreadableBook, bookID)
	}
	return book, nil
}

func (lake *fakeDatalake) ListBookIDs() ([]models.BookID, error) {
	lake.mutex.Lock()
	defer lake.mutex.Unlock()
	bookIDs := make([]models.BookID, 0, len(lake.books))
	for bookID := range lake.books {
		bookIDs = append(bookIDs, bookID)
	}
	slices.Sort(bookIDs)
	return bookIDs, nil
}

func newTestPublishedIndex(t *testing.T, lake *fakeDatalake) *PublishedIndex {
	t.Helper()
	published, err := NewPublishedIndex(lake, 4)
	if err != nil {
		t.Fatalf("NewPublishedIndex: %v", err)
	}
	return published
}

func assertBooksIndexed(t *testing.T, published *PublishedIndex, want int) {
	t.Helper()
	if got := published.Stats().BooksIndexed; got != want {
		t.Errorf("books indexed = %d, want %d", got, want)
	}
}

func TestIndexBookTwiceCreatesNoDuplicates(t *testing.T) {
	published := newTestPublishedIndex(t, newFakeDatalake(1342))
	for range 2 {
		if err := published.IndexBook(1342); err != nil {
			t.Fatalf("IndexBook: %v", err)
		}
	}
	assertBooksIndexed(t, published, 1)
	if postings := published.live.index.Postings("shared"); !slices.Equal(postings, []models.BookID{1342}) {
		t.Errorf("postings = %v, want [1342] exactly once", postings)
	}
}

func TestRebuildIndexesEveryBookFromScratch(t *testing.T) {
	lake := newFakeDatalake(11, 84, 1342)
	published := newTestPublishedIndex(t, lake)
	if err := published.IndexBook(1342); err != nil {
		t.Fatalf("IndexBook: %v", err)
	}
	lake.remove(1342)

	report, err := published.Rebuild(context.Background())

	if err != nil || report.BooksProcessed != 2 {
		t.Fatalf("report = %+v, err = %v; want 2 books processed", report, err)
	}
	assertBooksIndexed(t, published, 2)
	if postings := published.live.index.Postings("shared"); !slices.Equal(postings, []models.BookID{11, 84}) {
		t.Errorf("postings = %v, want [11 84]: a rebuild must drop books no longer in the datalake", postings)
	}
}

func TestRebuildKeepsUpdatesMadeWhileItRuns(t *testing.T) {
	lake := newFakeDatalake(11, 84)
	lake.holdWhenReading(84)
	published := newTestPublishedIndex(t, lake)
	rebuildErrors := make(chan error, 1)
	go func() {
		_, err := published.Rebuild(context.Background())
		rebuildErrors <- err
	}()
	<-lake.reachedHold // the rebuild has listed [11 84] and is now paused

	lake.add(2701) // ingested after the rebuild listed the datalake
	if err := published.IndexBook(2701); err != nil {
		t.Fatalf("IndexBook during rebuild: %v", err)
	}
	assertBooksIndexed(t, published, 1) // the old generation answers until the swap
	close(lake.releaseHold)

	if err := <-rebuildErrors; err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	assertBooksIndexed(t, published, 3)
}

func TestSecondRebuildWhileOneRunsIsRejected(t *testing.T) {
	lake := newFakeDatalake(11)
	lake.holdWhenReading(11)
	published := newTestPublishedIndex(t, lake)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = published.Rebuild(context.Background())
	}()
	<-lake.reachedHold

	_, err := published.Rebuild(context.Background())

	close(lake.releaseHold)
	<-firstDone
	if !errors.Is(err, ErrRebuildInProgress) {
		t.Errorf("err = %v, want ErrRebuildInProgress", err)
	}
}

func TestFailedRebuildKeepsPreviousIndexPublished(t *testing.T) {
	lake := newFakeDatalake(11, 84, 1342)
	published := newTestPublishedIndex(t, lake)
	if _, err := published.Rebuild(context.Background()); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	lake.markUnreadable(84)
	publishedAt := published.Stats().LastUpdate

	_, err := published.Rebuild(context.Background())

	if !errors.Is(err, errUnreadableBook) {
		t.Errorf("err = %v, want the read failure", err)
	}
	assertBooksIndexed(t, published, 3)
	if stats := published.Stats(); !stats.LastUpdate.Equal(publishedAt) || len(published.building) != 0 {
		t.Errorf("a failed rebuild must not publish anything: %+v", stats)
	}
}

func TestCancelledRebuildKeepsPreviousIndexPublished(t *testing.T) {
	published := newTestPublishedIndex(t, newFakeDatalake(11, 84))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := published.Rebuild(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	assertBooksIndexed(t, published, 0)
}

func TestNewPublishedIndexValidatesItsArguments(t *testing.T) {
	if _, err := NewPublishedIndex(nil, 1); err == nil {
		t.Error("a nil book source was accepted")
	}
	if _, err := NewPublishedIndex(newFakeDatalake(), 0); err == nil {
		t.Error("zero rebuild workers were accepted")
	}
}
