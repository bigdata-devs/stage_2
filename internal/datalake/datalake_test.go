package datalake

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/models"
)

func newTestDatalake(t *testing.T) Datalake {
	t.Helper()
	lake, err := New(t.TempDir(), DefaultBatchSize)
	if err != nil {
		t.Fatalf("create datalake: %v", err)
	}
	return lake
}

func sampleBook(bookID models.BookID) models.Book {
	return models.Book{ID: bookID, Header: "Title: Sample", Body: "It was a bright cold day in April."}
}

func TestNewRejectsNonPositiveBatchSize(t *testing.T) {
	if _, err := New(t.TempDir(), 0); err == nil {
		t.Fatal("expected an error for batch size 0")
	}
}

func TestBookDirectoryUsesBatchThenBookFolder(t *testing.T) {
	lake := newTestDatalake(t)
	cases := map[models.BookID]string{
		1:    filepath.Join("batch_0_999", "1"),
		999:  filepath.Join("batch_0_999", "999"),
		1342: filepath.Join("batch_1000_1999", "1342"),
		2000: filepath.Join("batch_2000_2999", "2000"),
	}
	for bookID, expectedSuffix := range cases {
		if directory := lake.BookDirectory(bookID); !strings.HasSuffix(directory, expectedSuffix) {
			t.Errorf("book %d: expected ...%s, got %s", bookID, expectedSuffix, directory)
		}
	}
}

func TestStoreWritesHeaderAndBodyThatReadBookReturns(t *testing.T) {
	lake := newTestDatalake(t)
	stored, err := lake.Store(sampleBook(1342))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if filepath.Base(stored.BodyPath) != "1342_body.txt" || filepath.Base(stored.HeaderPath) != "1342_header.txt" {
		t.Fatalf("unexpected file names: %+v", stored)
	}
	book, err := lake.ReadBook(1342)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if book != sampleBook(1342) {
		t.Fatalf("expected %+v, got %+v", sampleBook(1342), book)
	}
}

func TestStoreLeavesNoTemporaryFiles(t *testing.T) {
	lake := newTestDatalake(t)
	stored, err := lake.Store(sampleBook(84))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	entries, err := os.ReadDir(stored.Directory)
	if err != nil {
		t.Fatalf("list book folder: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected exactly the header and body, found %d entries", len(entries))
	}
}

func TestLocateReportsIncompleteBookAsNotFound(t *testing.T) {
	lake := newTestDatalake(t)
	stored, err := lake.Store(sampleBook(11))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := os.Remove(stored.HeaderPath); err != nil {
		t.Fatalf("remove header: %v", err)
	}
	if _, err := lake.Locate(11); !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("expected ErrBookNotFound, got %v", err)
	}
}

func TestListBookIDsReturnsOnlyCompleteBooksInOrder(t *testing.T) {
	lake := newTestDatalake(t)
	for _, bookID := range []models.BookID{2500, 84, 1342, 11} {
		if _, err := lake.Store(sampleBook(bookID)); err != nil {
			t.Fatalf("store %d: %v", bookID, err)
		}
	}
	if err := os.Remove(lake.filesOf(11).BodyPath); err != nil {
		t.Fatalf("remove body: %v", err)
	}
	bookIDs, err := lake.ListBookIDs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if expected := []models.BookID{84, 1342, 2500}; !slices.Equal(bookIDs, expected) {
		t.Fatalf("expected %v, got %v", expected, bookIDs)
	}
}

func TestListBookIDsOfMissingRootIsEmpty(t *testing.T) {
	lake, err := New(filepath.Join(t.TempDir(), "does-not-exist"), DefaultBatchSize)
	if err != nil {
		t.Fatalf("create datalake: %v", err)
	}
	bookIDs, err := lake.ListBookIDs()
	if err != nil || len(bookIDs) != 0 {
		t.Fatalf("expected an empty list without error, got %v, %v", bookIDs, err)
	}
}
