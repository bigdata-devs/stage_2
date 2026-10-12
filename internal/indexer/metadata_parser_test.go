package indexer

import (
	"slices"
	"testing"

	"boogle-engine/internal/models"
)

// The fixture and cases mirror tests/test_datamarts/test_header_parser.py from Stage 1.
const gutenbergHeader = "The Project Gutenberg eBook of Pride and Prejudice\r\r\n" +
	"    \r\r\n" +
	"This eBook is for the use of anyone anywhere in the United States and\r\r\n" +
	"at www.gutenberg.org. If you are not located in the United States,\r\r\n" +
	"\r\r\n" +
	"Title: The Works of Edgar Allan Poe\r\r\n" +
	"       Volume 1\r\r\n" +
	"\r\r\n" +
	"Author: Edgar Allan Poe\r\r\n" +
	"\r\r\n" +
	"Editor: Someone Else\r\r\n" +
	"\r\r\n" +
	"Release date: April 1, 2000 [eBook #2147]\r\r\n" +
	"                Most recently updated: May 5, 2021\r\r\n" +
	"\r\r\n" +
	"Language: English\r\r\n"

func TestParseHeaderFieldsJoinsContinuationLinesAndIgnoresPreamble(t *testing.T) {
	fields := parseHeaderFields(gutenbergHeader)
	if title := fields["title"]; !slices.Equal(title, []string{"The Works of Edgar Allan Poe Volume 1"}) {
		t.Errorf("unexpected title %q", title)
	}
	expectedRelease := []string{"April 1, 2000 [eBook #2147] Most recently updated: May 5, 2021"}
	if release := fields["release date"]; !slices.Equal(release, expectedRelease) {
		t.Errorf("unexpected release date %q", release)
	}
	if len(fields) != 5 {
		t.Errorf("expected 5 fields, got %d: %v", len(fields), fields)
	}
}

func TestExtractMetadataNormalizesFields(t *testing.T) {
	expected := models.BookMetadata{BookID: 2147, Title: "The Works of Edgar Allan Poe Volume 1", Author: "Edgar Allan Poe", Language: "en"}
	if metadata := ExtractMetadata(2147, gutenbergHeader); metadata != expected {
		t.Fatalf("expected %+v, got %+v", expected, metadata)
	}
}

func TestExtractMetadataJoinsSeveralAuthors(t *testing.T) {
	header := "Title: Joint Work\nAuthor: First Person\nAuthor: Second Person\nLanguage: French\n"
	if author := ExtractMetadata(1, header).Author; author != "First Person; Second Person" {
		t.Fatalf("unexpected author %q", author)
	}
}

func TestExtractMetadataMarksMissingFieldsUnknown(t *testing.T) {
	expected := models.BookMetadata{BookID: 1, Title: UnknownValue, Author: UnknownValue, Language: UnknownValue}
	if metadata := ExtractMetadata(1, "Other: A Committee\n"); metadata != expected {
		t.Fatalf("expected %+v, got %+v", expected, metadata)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"English":                     "en",
		"  german ":                   "de",
		"English, French and Spanish": "en,fr,es",
		"Greek, Ancient (to 1453)":    "grc",
		"Klingon":                     "klingon",
		"":                            UnknownValue,
	}
	for rawLanguage, expected := range cases {
		if code := NormalizeLanguage(rawLanguage); code != expected {
			t.Errorf("%q: expected %q, got %q", rawLanguage, expected, code)
		}
	}
}
