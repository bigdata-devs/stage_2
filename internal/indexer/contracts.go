package indexer

import (
	"errors"
	"time"

	"boogle-engine/internal/models"
)

// ErrRebuildInProgress is returned by Rebuild while another rebuild is still running.
var ErrRebuildInProgress = errors.New("an index rebuild is already running")

// BookSource is the read side of the datalake that an index is built from.
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
