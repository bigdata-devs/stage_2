package models

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidBookID = errors.New("invalid book id")

type BookID int

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

type Book struct {
	ID     BookID
	Header string
	Body   string
}

type StoredBook struct {
	ID         BookID
	Directory  string
	HeaderPath string
	BodyPath   string
}
