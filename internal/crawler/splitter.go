package crawler

import (
	"fmt"
	"strings"
	"unicode"

	"boogle-engine/internal/models"
)

// StartMarker and EndMarker delimit the body of every Project Gutenberg plain-text book.
const (
	StartMarker = "*** START OF THE PROJECT GUTENBERG EBOOK"
	EndMarker   = "*** END OF THE PROJECT GUTENBERG EBOOK"
)

const byteOrderMark = "\uFEFF"

// SplitBookText ports split_book_text: the header is everything before the START marker and
// the body everything between the START and END markers; the licence footer is dropped. The
// markers themselves are removed, but the rest of the START line (e.g. " PRIDE AND PREJUDICE ***")
// stays at the top of the body, exactly as in Stage 1. A missing marker makes the book unavailable.
func SplitBookText(bookID models.BookID, text string) (models.Book, error) {
	header, bodyAndFooter, found := strings.Cut(text, StartMarker)
	if !found {
		return models.Book{}, fmt.Errorf("%w: book %d has no START marker", ErrBookUnavailable, bookID)
	}
	body, _, found := strings.Cut(bodyAndFooter, EndMarker)
	if !found {
		return models.Book{}, fmt.Errorf("%w: book %d has no END marker after its START marker", ErrBookUnavailable, bookID)
	}
	return models.Book{
		ID:     bookID,
		Header: stripWhitespace(strings.TrimPrefix(header, byteOrderMark)),
		Body:   stripWhitespace(body),
	}, nil
}

// stripWhitespace behaves like Python's str.strip(). Go's unicode.IsSpace misses the ASCII
// separators U+001C..U+001F that Python treats as whitespace, so they are added explicitly.
func stripWhitespace(text string) string {
	return strings.TrimFunc(text, isPythonWhitespace)
}

func isPythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || (character >= 0x1C && character <= 0x1F)
}
