package control

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"boogle-engine/internal/models"
)

const (
	downloadedFileName   = "downloaded_books.txt"
	indexedFileName      = "indexed_books.txt"
	failedFileName       = "failed_books.txt"
	filePermissions      = 0o644
	directoryPermissions = 0o755
)

func appendLine(path string, line string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, filePermissions)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := lockExclusive(file); err != nil {
		return err
	}
	defer unlock(file)
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek %s: %w", path, err)
	}
	if _, err := file.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return nil
}

func atomicWrite(path string, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), filePermissions); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

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

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := lockShared(file); err != nil {
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

func sanitizeReason(reason string) string {
	sanitized := strings.Join(strings.Fields(reason), " ")
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}
