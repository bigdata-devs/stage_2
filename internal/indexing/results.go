package indexing

import (
	"errors"
	"time"
)

var ErrRebuildInProgress = errors.New("an index rebuild is already running")

type IndexStats struct {
	BooksIndexed int
	LastUpdate   time.Time
	SizeBytes    uint64
}

type RebuildReport struct {
	BooksProcessed int
	Elapsed        time.Duration
}
