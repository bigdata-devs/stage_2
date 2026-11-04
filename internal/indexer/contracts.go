package indexer

import (
	"errors"
	"time"

	"boogle-engine/internal/models"
)

var ErrRebuildInProgress = errors.New("an index rebuild is already running")

type BookSource interface {
	BookReader
	ListBookIDs() ([]models.BookID, error)
}

type IndexStats struct {
	BooksIndexed int
	LastUpdate   time.Time
	SizeBytes    uint64
}

type RebuildReport struct {
	BooksProcessed int
	Elapsed        time.Duration
}
