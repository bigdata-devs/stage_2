package localstate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

type Store struct {
	mutex      sync.RWMutex
	directory  string
	downloaded map[models.BookID]struct{}
	indexed    map[models.BookID]struct{}
	failed     map[models.BookID]string
}

func Load(directory string) (*Store, error) {
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
	return &Store{
		directory:  directory,
		downloaded: downloaded,
		indexed:    indexed,
		failed:     failed,
	}, nil
}

func (state *Store) RecordDownloaded(bookID models.BookID) error {
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

func (state *Store) RecordIndexed(bookID models.BookID) error {
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

func (state *Store) RecordFailed(bookID models.BookID, reason string) error {
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

func (state *Store) IsDownloaded(bookID models.BookID) bool {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	_, recorded := state.downloaded[bookID]
	return recorded
}

func (state *Store) IsIndexed(bookID models.BookID) bool {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	_, recorded := state.indexed[bookID]
	return recorded
}

func (state *Store) FailureReason(bookID models.BookID) (string, bool) {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	reason, recorded := state.failed[bookID]
	return reason, recorded
}

func (state *Store) DownloadedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.downloaded)
}

func (state *Store) IndexedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.indexed)
}

func (state *Store) FailedCount() int {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	return len(state.failed)
}

func (state *Store) IndexedIDs() []models.BookID {
	state.mutex.RLock()
	defer state.mutex.RUnlock()
	ids := make([]models.BookID, 0, len(state.indexed))
	for bookID := range state.indexed {
		ids = append(ids, bookID)
	}
	slices.Sort(ids)
	return ids
}

func (state *Store) WipeFailures() ([]models.BookID, error) {
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
