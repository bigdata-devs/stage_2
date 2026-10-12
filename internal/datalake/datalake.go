// Package datalake stores raw books on disk using the batch-based layout chosen in Stage 1:
//
//	<root>/batch_<LOW>_<HIGH>/<book_id>/<book_id>_header.txt
//	<root>/batch_<LOW>_<HIGH>/<book_id>/<book_id>_body.txt
//
// Book 1342 with the default batch size of 1000 lives in "batch_1000_1999/1342/".
// A book is complete (ready for indexing) once both files exist. Every file is written
// atomically, so a reader never sees a truncated header or body.
package datalake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"boogle-engine/internal/models"
)

// DefaultBatchSize is the number of consecutive book IDs grouped in one batch folder.
const DefaultBatchSize = 1000

const (
	headerFileSuffix     = "_header.txt"
	bodyFileSuffix       = "_body.txt"
	directoryPermissions = 0o755
)

// ErrBookNotFound is returned when the datalake holds no complete copy of a book.
var ErrBookNotFound = errors.New("book not found in datalake")

// Datalake is a root folder organized in batch folders of consecutive book IDs.
// Its methods are safe for concurrent use by several goroutines and processes.
type Datalake struct {
	root      string
	batchSize int
}

// New returns a datalake rooted at root. The folder is created lazily on the first write.
func New(root string, batchSize int) (Datalake, error) {
	if batchSize < 1 {
		return Datalake{}, fmt.Errorf("batch size must be a positive integer, got %d", batchSize)
	}
	return Datalake{root: root, batchSize: batchSize}, nil
}

// BookDirectory returns the folder of a book, e.g. "<root>/batch_1000_1999/1342".
func (lake Datalake) BookDirectory(bookID models.BookID) string {
	lowerBound := (int(bookID) / lake.batchSize) * lake.batchSize
	upperBound := lowerBound + lake.batchSize - 1
	batchFolder := fmt.Sprintf("batch_%d_%d", lowerBound, upperBound)
	return filepath.Join(lake.root, batchFolder, bookID.String())
}

// Store writes the body and then the header of a book into its folder, replacing any previous copy.
func (lake Datalake) Store(book models.Book) (models.StoredBook, error) {
	stored := lake.filesOf(book.ID)
	if err := os.MkdirAll(stored.Directory, directoryPermissions); err != nil {
		return models.StoredBook{}, fmt.Errorf("create folder of book %d: %w", book.ID, err)
	}
	if err := writeFileAtomically(stored.BodyPath, book.Body); err != nil {
		return models.StoredBook{}, err
	}
	if err := writeFileAtomically(stored.HeaderPath, book.Header); err != nil {
		return models.StoredBook{}, err
	}
	return stored, nil
}

// Locate returns the files of a complete book, or ErrBookNotFound when either file is missing.
func (lake Datalake) Locate(bookID models.BookID) (models.StoredBook, error) {
	stored := lake.filesOf(bookID)
	if isRegularFile(stored.HeaderPath) && isRegularFile(stored.BodyPath) {
		return stored, nil
	}
	return models.StoredBook{}, fmt.Errorf("%w: book %d in %s", ErrBookNotFound, bookID, stored.Directory)
}

// ReadBook loads the header and body of a complete book.
func (lake Datalake) ReadBook(bookID models.BookID) (models.Book, error) {
	stored, err := lake.Locate(bookID)
	if err != nil {
		return models.Book{}, err
	}
	header, err := os.ReadFile(stored.HeaderPath)
	if err != nil {
		return models.Book{}, fmt.Errorf("read header of book %d: %w", bookID, err)
	}
	body, err := os.ReadFile(stored.BodyPath)
	if err != nil {
		return models.Book{}, fmt.Errorf("read body of book %d: %w", bookID, err)
	}
	return models.Book{ID: bookID, Header: string(header), Body: string(body)}, nil
}

func (lake Datalake) filesOf(bookID models.BookID) models.StoredBook {
	directory := lake.BookDirectory(bookID)
	return models.StoredBook{
		ID:         bookID,
		Directory:  directory,
		HeaderPath: filepath.Join(directory, bookID.String()+headerFileSuffix),
		BodyPath:   filepath.Join(directory, bookID.String()+bodyFileSuffix),
	}
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
