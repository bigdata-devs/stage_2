package datalake

import (
	"fmt"
	"os"
	"path/filepath"

	"boogle-engine/internal/models"
)

const DefaultBatchSize = 1000

const (
	headerFileSuffix     = "_header.txt"
	bodyFileSuffix       = "_body.txt"
	directoryPermissions = 0o755
)

type Datalake struct {
	root      string
	batchSize int
}

func New(root string, batchSize int) (Datalake, error) {
	if batchSize < 1 {
		return Datalake{}, fmt.Errorf("batch size must be a positive integer, got %d", batchSize)
	}
	return Datalake{root: root, batchSize: batchSize}, nil
}

func (lake Datalake) BookDirectory(bookID models.BookID) string {
	lowerBound := (int(bookID) / lake.batchSize) * lake.batchSize
	upperBound := lowerBound + lake.batchSize - 1
	batchFolder := fmt.Sprintf("batch_%d_%d", lowerBound, upperBound)
	return filepath.Join(lake.root, batchFolder, bookID.String())
}

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

func (lake Datalake) Locate(bookID models.BookID) (models.StoredBook, error) {
	stored := lake.filesOf(bookID)
	if isRegularFile(stored.HeaderPath) && isRegularFile(stored.BodyPath) {
		return stored, nil
	}
	return models.StoredBook{}, fmt.Errorf("%w: book %d in %s", models.ErrBookNotFound, bookID, stored.Directory)
}

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
