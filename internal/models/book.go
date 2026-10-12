// Package models holds the domain types shared by every Boogle Engine service.
package models

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidBookID is returned when a value cannot be read as a positive numeric book ID.
var ErrInvalidBookID = errors.New("invalid book id")

// BookID identifies a Project Gutenberg book. Valid IDs are positive integers.
type BookID int

// ParseBookID reads a book ID the way Stage 1 normalize_book_id did: surrounding
// spaces and leading zeros are accepted (" 0042 " -> 42), while signs, decimals,
// non-ASCII digits and zero are rejected.
func ParseBookID(raw string) (BookID, error) {
	candidate := strings.TrimSpace(raw)
	if !isASCIIDigits(candidate) {
		return 0, fmt.Errorf("%w: %q is not a plain number", ErrInvalidBookID, raw)
	}
	value, err := strconv.Atoi(candidate)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrInvalidBookID, raw, err)
	}
	if value == 0 {
		return 0, fmt.Errorf("%w: %q must be positive", ErrInvalidBookID, raw)
	}
	return BookID(value), nil
}

// String returns the canonical decimal form of the ID, e.g. "1342".
func (id BookID) String() string {
	return strconv.Itoa(int(id))
}

func isASCIIDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// Book is a downloaded Project Gutenberg book split into its licence header and its text body.
type Book struct {
	ID     BookID
	Header string
	Body   string
}

// StoredBook locates the files of a complete book copy inside the datalake.
type StoredBook struct {
	ID         BookID
	Directory  string
	HeaderPath string
	BodyPath   string
}
