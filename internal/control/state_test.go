package control

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"boogle-engine/internal/models"
)

func newTestState(t *testing.T) *ControlState {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "control"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return state
}

func TestLoadStateTreatsMissingDirectoryAsEmpty(t *testing.T) {
	state := newTestState(t)
	if state.DownloadedCount() != 0 || state.IndexedCount() != 0 || state.FailedCount() != 0 {
		t.Fatalf("expected empty state, got %d downloaded, %d indexed, %d failed",
			state.DownloadedCount(), state.IndexedCount(), state.FailedCount())
	}
}

func TestRecordsRoundTripThroughFiles(t *testing.T) {
	state := newTestState(t)
	if err := state.RecordDownloaded(1342); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := state.RecordIndexed(84); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := state.RecordFailed(5, "unavailable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reloaded, err := LoadState(state.directory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reloaded.IsDownloaded(1342) || !reloaded.IsIndexed(84) {
		t.Fatal("expected book 1342 downloaded and book 84 indexed after reload")
	}
	reason, found := reloaded.FailureReason(5)
	if !found || reason != "unavailable" {
		t.Fatalf("expected failure reason %q, got %q (found=%v)", "unavailable", reason, found)
	}
}

func TestRecordIsIdempotent(t *testing.T) {
	state := newTestState(t)
	for range 3 {
		if err := state.RecordDownloaded(1342); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	content := readControlFile(t, state.directory, downloadedFileName)
	if content != "1342\n" {
		t.Fatalf("expected a single line, got %q", content)
	}
}

func TestLoadStateSkipsMalformedLines(t *testing.T) {
	state := newTestState(t)
	writeControlFile(t, state.directory, downloadedFileName, "1342\n\nfoo\n0042\n84\n")
	writeControlFile(t, state.directory, failedFileName, "5\tunavailable\nbroken\n-3\tboom\n")

	reloaded, err := LoadState(state.directory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reloaded.DownloadedCount() != 3 {
		t.Fatalf("expected 3 downloaded books (1342, 42 and 84), got %d", reloaded.DownloadedCount())
	}
	if !reloaded.IsDownloaded(42) {
		t.Fatal("expected leading-zero ID 0042 to load as book 42")
	}
	reason, found := reloaded.FailureReason(5)
	if !found || reason != "unavailable" {
		t.Fatalf("expected failure reason %q, got %q (found=%v)", "unavailable", reason, found)
	}
	if _, found := reloaded.FailureReason(3); found {
		t.Fatal("expected negative book ID to be skipped")
	}
}

func TestLastFailureReasonWinsOnLoad(t *testing.T) {
	state := newTestState(t)
	writeControlFile(t, state.directory, failedFileName, "5\tfirst\n5\tsecond\n")

	reloaded, err := LoadState(state.directory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reason, found := reloaded.FailureReason(5)
	if !found || reason != "second" {
		t.Fatalf("expected reason %q, got %q (found=%v)", "second", reason, found)
	}
}

func TestRecordFailedSanitizesReason(t *testing.T) {
	state := newTestState(t)
	if err := state.RecordFailed(1342, "  multi\tline\n reason  "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content := readControlFile(t, state.directory, failedFileName)
	if content != "1342\tmulti line reason\n" {
		t.Fatalf("expected sanitized reason, got %q", content)
	}
}

func TestConcurrentRecordsNeverLoseLines(t *testing.T) {
	const workers = 4
	const booksPerWorker = 25
	state := newTestState(t)

	var group sync.WaitGroup
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for book := range booksPerWorker {
				bookID := models.BookID(worker*booksPerWorker + book)
				if err := state.RecordDownloaded(bookID); err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}
	group.Wait()

	if state.DownloadedCount() != workers*booksPerWorker {
		t.Fatalf("expected %d books, got %d", workers*booksPerWorker, state.DownloadedCount())
	}
	lines := strings.Split(strings.TrimSuffix(readControlFile(t, state.directory, downloadedFileName), "\n"), "\n")
	if len(lines) != workers*booksPerWorker {
		t.Fatalf("expected %d lines in the file, got %d", workers*booksPerWorker, len(lines))
	}
}

func readControlFile(t *testing.T, directory string, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

func writeControlFile(t *testing.T, directory string, name string, content string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), filePermissions); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func BenchmarkRecordDownloaded(b *testing.B) {
	state := newBenchmarkState(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := state.RecordDownloaded(models.BookID(1_000_000 + i)); err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}

func BenchmarkRecordDownloadedConcurrent(b *testing.B) {
	state := newBenchmarkState(b)
	var nextBookID atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bookID := models.BookID(10_000_000 + nextBookID.Add(1))
			if err := state.RecordDownloaded(bookID); err != nil {
				b.Errorf("unexpected error: %v", err)
				return
			}
		}
	})
}

func BenchmarkLoadState(b *testing.B) {
	state := newBenchmarkState(b)
	for i := 0; i < 1000; i++ {
		if err := state.RecordDownloaded(models.BookID(i)); err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadState(state.directory); err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}

func newBenchmarkState(b *testing.B) *ControlState {
	b.Helper()
	state, err := LoadState(filepath.Join(b.TempDir(), "control"))
	if err != nil {
		b.Fatalf("unexpected error: %v", err)
	}
	return state
}
