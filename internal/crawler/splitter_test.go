package crawler

import (
	"errors"
	"strings"
	"testing"
)

// Same fixture as tests/test_datalake/fakes.py and the Stage 1 Go splitter tests.
const (
	sampleHeader = "Title: Sample Book\nAuthor: Jane Doe"
	sampleBody   = "It was a bright cold day in April."
	sampleText   = sampleHeader + "\n" + StartMarker + " SAMPLE ***\n" + sampleBody + "\n" + EndMarker + " SAMPLE ***\nLicense footer"
)

func TestSplitBookTextSeparatesHeaderAndBodyAndDropsFooter(t *testing.T) {
	book, err := SplitBookText(1, sampleText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book.Header != sampleHeader {
		t.Fatalf("unexpected header %q", book.Header)
	}
	if expectedBody := "SAMPLE ***\n" + sampleBody; book.Body != expectedBody {
		t.Fatalf("unexpected body %q", book.Body)
	}
	if strings.Contains(book.Body, "License footer") {
		t.Fatal("footer leaked into the body")
	}
}

func TestSplitBookTextRemovesByteOrderMark(t *testing.T) {
	book, err := SplitBookText(1, "\uFEFF"+sampleText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if book.Header != sampleHeader {
		t.Fatalf("unexpected header %q", book.Header)
	}
}

func TestSplitBookTextWithoutStartMarkerIsUnavailable(t *testing.T) {
	if _, err := SplitBookText(1, "no markers at all"); !errors.Is(err, ErrBookUnavailable) {
		t.Fatalf("expected ErrBookUnavailable, got %v", err)
	}
}

func TestSplitBookTextWithEndMarkerBeforeStartIsUnavailable(t *testing.T) {
	text := EndMarker + " ***\nheader\n" + StartMarker + " ***\nbody"
	if _, err := SplitBookText(1, text); !errors.Is(err, ErrBookUnavailable) {
		t.Fatalf("expected ErrBookUnavailable, got %v", err)
	}
}
