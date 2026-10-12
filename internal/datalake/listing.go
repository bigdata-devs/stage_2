package datalake

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"boogle-engine/internal/models"
)

var batchFolderPattern = regexp.MustCompile(`^batch_\d+_\d+$`)

// ListBookIDs returns, in ascending order, the ID of every complete book in the datalake.
// A missing root folder simply means an empty datalake.
func (lake Datalake) ListBookIDs() ([]models.BookID, error) {
	batchDirectories, err := lake.batchDirectories()
	if err != nil {
		return []models.BookID{}, err
	}
	bookIDs := []models.BookID{}
	for _, batchDirectory := range batchDirectories {
		batchBookIDs, err := lake.completeBooksIn(batchDirectory)
		if err != nil {
			return []models.BookID{}, err
		}
		bookIDs = append(bookIDs, batchBookIDs...)
	}
	slices.Sort(bookIDs)
	return bookIDs, nil
}

func (lake Datalake) batchDirectories() ([]string, error) {
	entries, err := os.ReadDir(lake.root)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return []string{}, fmt.Errorf("list datalake %s: %w", lake.root, err)
	}
	directories := []string{}
	for _, entry := range entries {
		if entry.IsDir() && batchFolderPattern.MatchString(entry.Name()) {
			directories = append(directories, filepath.Join(lake.root, entry.Name()))
		}
	}
	return directories, nil
}

// completeBooksIn returns the books of one batch folder whose header and body both exist.
// Folders that are not book IDs, or that sit in the wrong batch, are ignored.
func (lake Datalake) completeBooksIn(batchDirectory string) ([]models.BookID, error) {
	entries, err := os.ReadDir(batchDirectory)
	if err != nil {
		return []models.BookID{}, fmt.Errorf("list batch folder %s: %w", batchDirectory, err)
	}
	bookIDs := []models.BookID{}
	for _, entry := range entries {
		bookID, err := models.ParseBookID(entry.Name())
		if err == nil && entry.IsDir() && lake.isComplete(bookID) {
			bookIDs = append(bookIDs, bookID)
		}
	}
	return bookIDs, nil
}

func (lake Datalake) isComplete(bookID models.BookID) bool {
	_, err := lake.Locate(bookID)
	return err == nil
}
