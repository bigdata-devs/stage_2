// Package control implements the Control Module of Boogle Engine: the process that
// orchestrates the Ingestion and Indexing Services over HTTP and records pipeline
// progress so a restart resumes exactly where the previous run stopped.
package control

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

// ControlState is the durable record of pipeline progress: which books were downloaded,
// which ones were indexed, and why the rest failed. It is backed by append-only text
// files under one directory — downloaded_books.txt and indexed_books.txt hold one book
// ID per line, failed_books.txt holds "<book_id>\t<reason>" — with every append
// protected by flock. The Control Module is the only writer of those files. Methods are
// safe for concurrent use by several goroutines.
type ControlState struct {
	mutex      sync.RWMutex
	directory  string
	downloaded map[models.BookID]struct{}
	indexed    map[models.BookID]struct{}
	failed     map[models.BookID]string
}

// LoadState rebuilds the sets from the control directory. A missing directory or file
// means "nothing recorded yet". Malformed lines are skipped: replaying an idempotent
// service call is safe, losing an id would not be.
func LoadState(directory string) (*ControlState, error) {
	if err := os.MkdirAll(directory, directoryPermissions); err != nil {
		return nil, fmt.Errorf("create control directory: %w", err)
	}
	downloaded, err := readBookIDs(filepath.Join(directory, downloadedFileName))
	if err != nil {
		return nil, err
	}
	indexed, err := readBookIDs(filepath.Join(directory, indexedFileName))
	if err != nil {
		return nil, err
	}
	failed, err := readFailures(filepath.Join(directory, failedFileName))
	if err != nil {
		return nil, err
	}
	return &ControlState{
		directory:  directory,
		downloaded: downloaded,
		indexed:    indexed,
		failed:     failed,
	}, nil
}

// RecordDownloaded marks a book as stored in the datalake. Repeating the call for the
// same book appends nothing.
func (state *ControlState) RecordDownloaded(bookID models.BookID) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if _, recorded := state.downloaded[bookID]; recorded {
		return nil
	}
	line := bookID.String()
	if err := appendLine(filepath.Join(state.directory, downloadedFileName), line); err != nil {
		return fmt.Errorf("record book %d as downloaded: %w", bookID, err)
	}
	state.downloaded[bookID] = struct{}{}
	return nil
}

// RecordIndexed marks a book as processed by the Indexing Service. Repeating the call
// for the same book appends nothing.
func (state *ControlState) RecordIndexed(bookID models.BookID) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if _, recorded := state.indexed[bookID]; recorded {
		return nil
	}
	line := bookID.String()
	if err := appendLine(filepath.Join(state.directory, indexedFileName), line); err != nil {
		return fmt.Errorf("record book %d as indexed: %w", bookID, err)
	}
	state.indexed[bookID] = struct{}{}
	return nil
}

// RecordFailed stores why a book left the pipeline. The first recorded reason wins;
// repeating the call for the same book appends nothing.
func (state *ControlState) RecordFailed(bookID models.BookID, reason string) error {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if _, recorded := state.failed[bookID]; recorded {
		return nil
	}
	sanitized := sanitizeReason(reason)
	line := bookID.String() + "\t" + sanitized
	if err := appendLine(filepath.Join(state.directory, failedFileName), line); err != nil {
		return fmt.Errorf("record book %d as failed: %w", bookID, err)
	}
	state.failed[bookID] = sanitized
	return nil
}

// IsDownloaded reports whether a book was ever recorded as stored in the datalake.
func (state *ControlState) IsDownloaded(bookID models.BookID) bool {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	_, recorded := state.downloaded[bookID]
	return recorded
}

// IsIndexed reports whether a book was ever recorded as fully indexed.
func (state *ControlState) IsIndexed(bookID models.BookID) bool {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	_, recorded := state.indexed[bookID]
	return recorded
}

// FailureReason returns the recorded failure reason of a book, if any.
func (state *ControlState) FailureReason(bookID models.BookID) (string, bool) {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	reason, recorded := state.failed[bookID]
	return reason, recorded
}

// DownloadedCount returns how many distinct books were recorded as downloaded.
func (state *ControlState) DownloadedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.downloaded)
}

// IndexedCount returns how many distinct books were recorded as indexed.
func (state *ControlState) IndexedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.indexed)
}

// FailedCount returns how many distinct books were recorded as failed.
func (state *ControlState) FailedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.failed)
}

// IndexedIDs returns a sorted copy of every book ID recorded as indexed.
func (state *ControlState) IndexedIDs() []models.BookID {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	ids := make([]models.BookID, 0, len(state.indexed))
	for bookID := range state.indexed {
		ids = append(ids, bookID)
	}
	slices.Sort(ids)
	return ids
}

// WipeFailures atomically empties failed_books.txt and clears the in-memory failed
// set, returning the IDs that were cleared so the caller can re-enqueue them. It
// is the backing call of the -retry-failed flag: a crash during the wipe leaves
// either the old content or an empty file, never a half-written one.
func (state *ControlState) WipeFailures() ([]models.BookID, error) {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if len(state.failed) == 0 {
		return []models.BookID{}, nil
	}
	cleared := make([]models.BookID, 0, len(state.failed))
	for bookID := range state.failed {
		cleared = append(cleared, bookID)
	}
	slices.Sort(cleared)
	path := filepath.Join(state.directory, failedFileName)
	if err := atomicWrite(path, ""); err != nil {
		return nil, fmt.Errorf("wipe %s: %w", failedFileName, err)
	}
	state.failed = map[models.BookID]string{}
	return cleared, nil
}
