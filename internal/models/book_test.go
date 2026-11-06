package models

import (
	"errors"
	"testing"
)

func TestParseBookIDStripsWhitespaceAndLeadingZeros(t *testing.T) {
	bookID, err := ParseBookID(" 0042 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bookID != 42 {
		t.Fatalf("expected 42, got %d", bookID)
	}
}

func TestParseBookIDRejectsMalformedValues(t *testing.T) {
	malformedValues := []string{"", "   ", "abc", "12a", "-3", "+3", "1.5", "1 2", "0", "000", "\u0663", "99999999999999999999"}
	for _, malformedValue := range malformedValues {
		t.Run(malformedValue, func(t *testing.T) {
			if _, err := ParseBookID(malformedValue); !errors.Is(err, ErrInvalidBookID) {
				t.Fatalf("expected ErrInvalidBookID for %q, got %v", malformedValue, err)
			}
		})
	}
}

func TestBookIDStringIsCanonicalDecimal(t *testing.T) {
	if text := BookID(1342).String(); text != "1342" {
		t.Fatalf("expected \"1342\", got %q", text)
	}
}
