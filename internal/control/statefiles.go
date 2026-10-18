package control

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"boogle-engine/internal/models"
)

const (
	downloadedFileName   = "downloaded_books.txt"
	indexedFileName      = "indexed_books.txt"
	failedFileName       = "failed_books.txt"
	filePermissions      = 0o644
	directoryPermissions = 0o755
)

// appendLine adds one line to a control file under an exclusive flock, so concurrent
// writers — goroutines inside this process or a second process — never interleave
// partial lines.
func appendLine(path string, line string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePermissions)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := lock(file, syscall.LOCK_EX); err != nil {
		return err
	}
	defer unlock(file)
	if _, err := file.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return nil
}

// readBookIDs loads a one-ID-per-line file into a set. A missing file counts as empty
// and malformed lines are skipped.
func readBookIDs(path string) (map[models.BookID]struct{}, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	books := make(map[models.BookID]struct{}, len(lines))
	for _, line := range lines {
		bookID, err := models.ParseBookID(line)
		if err != nil {
			continue
		}
		books[bookID] = struct{}{}
	}
	return books, nil
}

// readFailures loads the "book_id<TAB>reason" file into a map. When a book appears
// twice, the last recorded reason wins.
func readFailures(path string) (map[models.BookID]string, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	failures := make(map[models.BookID]string, len(lines))
	for _, line := range lines {
		rawID, reason, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		bookID, err := models.ParseBookID(rawID)
		if err != nil {
			continue
		}
		failures[bookID] = sanitizeReason(reason)
	}
	return failures, nil
}

// readLines returns the lines of a control file under a shared flock; a missing file
// yields an empty slice, never a nil one.
func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := lock(file, syscall.LOCK_SH); err != nil {
		return nil, err
	}
	defer unlock(file)
	return scanLines(file)
}

func scanLines(file *os.File) ([]string, error) {
	lines := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", file.Name(), err)
	}
	return lines, nil
}

// sanitizeReason flattens a failure reason into one tab-free, single-spaced line and
// never returns an empty string.
func sanitizeReason(reason string) string {
	sanitized := strings.Join(strings.Fields(reason), " ")
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}

func lock(file *os.File, mode int) error {
	if err := syscall.Flock(int(file.Fd()), mode); err != nil {
		return fmt.Errorf("lock %s: %w", file.Name(), err)
	}
	return nil
}

// unlock releases the flock; the deferred file.Close would release it anyway.
func unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
