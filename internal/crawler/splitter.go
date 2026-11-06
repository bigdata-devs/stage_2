package crawler

import (
	"fmt"
	"strings"
	"unicode"

	"boogle-engine/internal/models"
)

const (
	StartMarker = "*** START OF THE PROJECT GUTENBERG EBOOK"
	EndMarker   = "*** END OF THE PROJECT GUTENBERG EBOOK"
)

const byteOrderMark = "\uFEFF"

func SplitBookText(bookID models.BookID, text string) (models.Book, error) {
	header, bodyAndFooter, found := strings.Cut(text, StartMarker)
	if !found {
		return models.Book{}, fmt.Errorf("%w: book %d has no START marker", models.ErrBookUnavailable, bookID)
	}
	body, _, found := strings.Cut(bodyAndFooter, EndMarker)
	if !found {
		return models.Book{}, fmt.Errorf("%w: book %d has no END marker after its START marker", models.ErrBookUnavailable, bookID)
	}
	return models.Book{
		ID:     bookID,
		Header: stripWhitespace(strings.TrimPrefix(header, byteOrderMark)),
		Body:   stripWhitespace(body),
	}, nil
}

func stripWhitespace(text string) string {
	return strings.TrimFunc(text, isPythonWhitespace)
}

func isPythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || (character >= 0x1C && character <= 0x1F)
}
