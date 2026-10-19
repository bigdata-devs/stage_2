package mongodb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

// These tests exercise a live server. They skip automatically when none is reachable, so the
// suite stays green on machines without MongoDB (the same policy as the Stage 1 Python tests).

func TestSearchBooksRanksByTermFrequency(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Term: "adventure", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected total 2, got %d", result.Total)
	}
	assertBookIDs(t, result.Books, []models.BookID{2, 1})
}

func TestSearchBooksBreaksRelevanceTiesByBookID(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Term: "sea", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected total 2, got %d", result.Total)
	}
	assertBookIDs(t, result.Books, []models.BookID{1, 4})
}

func TestSearchBooksPaginatesWithoutChangingTotal(t *testing.T) {
	reader := seededReader(t)
	pages := []struct {
		offset int
		limit  int
		want   []models.BookID
	}{
		{offset: 0, limit: 1, want: []models.BookID{2}},
		{offset: 1, limit: 1, want: []models.BookID{1}},
		{offset: 5, limit: 10, want: []models.BookID{}},
	}

	for _, page := range pages {
		result, err := reader.SearchBooks(context.Background(),
			SearchRequest{Term: "adventure", Offset: page.offset, Limit: page.limit})
		if err != nil {
			t.Fatalf("offset %d: unexpected error: %v", page.offset, err)
		}
		if result.Total != 2 {
			t.Fatalf("offset %d: expected total 2, got %d", page.offset, result.Total)
		}
		assertBookIDs(t, result.Books, page.want)
	}
}

func TestSearchBooksWithholdsPendingBooks(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Term: "castaway", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected total 0, got %d", result.Total)
	}
	assertBookIDs(t, result.Books, []models.BookID{})
}

func TestSearchBooksFiltersByLanguageList(t *testing.T) {
	reader := seededReader(t)
	cases := []struct {
		language string
		want     []models.BookID
	}{
		{language: "fr", want: []models.BookID{2}},
		{language: "en", want: []models.BookID{2, 1}},
	}

	for _, testCase := range cases {
		result, err := reader.SearchBooks(context.Background(),
			SearchRequest{Term: "adventure", Language: testCase.language, Limit: 10})
		if err != nil {
			t.Fatalf("language %q: unexpected error: %v", testCase.language, err)
		}
		if result.Total != len(testCase.want) {
			t.Fatalf("language %q: expected total %d, got %d", testCase.language, len(testCase.want), result.Total)
		}
		assertBookIDs(t, result.Books, testCase.want)
	}
}

func TestSearchBooksFiltersByAuthorIgnoringCaseAndSubstring(t *testing.T) {
	reader := seededReader(t)
	cases := []struct {
		author string
		want   []models.BookID
	}{
		{author: "AUSTEN", want: []models.BookID{1}},
		{author: "ane", want: []models.BookID{2, 1}},
	}

	for _, testCase := range cases {
		result, err := reader.SearchBooks(context.Background(),
			SearchRequest{Term: "adventure", Author: testCase.author, Limit: 10})
		if err != nil {
			t.Fatalf("author %q: unexpected error: %v", testCase.author, err)
		}
		if result.Total != len(testCase.want) {
			t.Fatalf("author %q: expected total %d, got %d", testCase.author, len(testCase.want), result.Total)
		}
		assertBookIDs(t, result.Books, testCase.want)
	}
}

func TestSearchBooksFiltersByExactYear(t *testing.T) {
	reader := seededReader(t)
	cases := []struct {
		year int
		want []models.BookID
	}{
		{year: 1850, want: []models.BookID{2}},
		{year: 1999, want: []models.BookID{}},
	}

	for _, testCase := range cases {
		result, err := reader.SearchBooks(context.Background(),
			SearchRequest{Term: "adventure", Year: testCase.year, Limit: 10})
		if err != nil {
			t.Fatalf("year %d: unexpected error: %v", testCase.year, err)
		}
		if result.Total != len(testCase.want) {
			t.Fatalf("year %d: expected total %d, got %d", testCase.year, len(testCase.want), result.Total)
		}
		assertBookIDs(t, result.Books, testCase.want)
	}
}

func TestSearchBooksCombinesFiltersAsConjunction(t *testing.T) {
	reader := seededReader(t)
	cases := []struct {
		name    string
		request SearchRequest
		want    []models.BookID
	}{
		{
			name:    "language and year keep the english book out",
			request: SearchRequest{Term: "adventure", Author: "ane", Language: "fr", Year: 1850, Limit: 10},
			want:    []models.BookID{2},
		},
		{
			name:    "year keeps the french book out",
			request: SearchRequest{Term: "adventure", Author: "ane", Language: "en", Year: 1813, Limit: 10},
			want:    []models.BookID{1},
		},
	}

	for _, testCase := range cases {
		result, err := reader.SearchBooks(context.Background(), testCase.request)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", testCase.name, err)
		}
		if result.Total != len(testCase.want) {
			t.Fatalf("%s: expected total %d, got %d", testCase.name, len(testCase.want), result.Total)
		}
		assertBookIDs(t, result.Books, testCase.want)
	}
}

func TestSearchBooksWithUnknownTermReturnsEmptyList(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Term: "zzzz", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected total 0, got %d", result.Total)
	}
	assertBookIDs(t, result.Books, []models.BookID{})
}

func TestSearchBooksRejectsInvalidRequests(t *testing.T) {
	reader := NewDatamartReader(Client{})
	cases := []struct {
		name    string
		request SearchRequest
	}{
		{name: "empty term", request: SearchRequest{Limit: 10}},
		{name: "negative offset", request: SearchRequest{Term: "adventure", Offset: -1, Limit: 10}},
		{name: "zero limit", request: SearchRequest{Term: "adventure"}},
		{name: "negative year", request: SearchRequest{Term: "adventure", Year: -1, Limit: 10}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := reader.SearchBooks(context.Background(), testCase.request)
			if !errors.Is(err, ErrInvalidSearchRequest) {
				t.Fatalf("expected ErrInvalidSearchRequest, got %v", err)
			}
		})
	}
}

func seededReader(t *testing.T) DatamartReader {
	t.Helper()
	client := liveClient(t)
	resetDatamart(t, client)
	seedSearchBooks(t, client)
	return NewDatamartReader(client)
}

// seedSearchBooks mirrors a realistic corpus: three published books that share terms with
// different frequencies and languages, plus a book left pending mid-indexing that carries a term
// no published book has. The pending book is seeded first so the published books merge their
// postings into its term documents, as they would after a retried indexing run.
func seedSearchBooks(t *testing.T, client Client) {
	t.Helper()
	seedPendingBook(t, client, metadataInput(3, "Mark Twain", "es", 1901,
		"adventure", "castaway", "castaway"))

	indexer := NewBookIndexer(client)
	published := []models.IndexBookInput{
		metadataInput(1, "Jane Austen", "en", 1813, "adventure", "adventure", "island", "sea"),
		metadataInput(2, "jane austin", "en,fr", 1850,
			"adventure", "adventure", "adventure", "adventure", "adventure", "map"),
		metadataInput(4, "Herman Melville", "en", 1851, "whale", "sea"),
	}
	for _, input := range published {
		if err := indexer.IndexBook(context.Background(), input); err != nil {
			t.Fatalf("book %d: unexpected error: %v", input.Metadata.BookID, err)
		}
	}
}

// seedPendingBook reproduces a book whose indexing stopped between the posting write and the
// publish step: metadata recorded as pending and term postings already present.
func seedPendingBook(t *testing.T, client Client, input models.IndexBookInput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := writeMetadata(ctx, client.Database().Collection(BooksCollection), input.Metadata, StatusPending); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}
	index := client.Database().Collection(InvertedIndexCollection)
	for term, frequency := range termFrequencies(input.Tokens) {
		document := bson.D{
			{Key: "_id", Value: term},
			{Key: "postings", Value: bson.A{bson.D{
				{Key: "book_id", Value: input.Metadata.BookID},
				{Key: "tf", Value: frequency},
			}}},
		}
		if _, err := index.InsertOne(ctx, document); err != nil {
			t.Fatalf("insert postings for %q: %v", term, err)
		}
	}
}

func metadataInput(bookID models.BookID, author, language string, year int, tokens ...string) models.IndexBookInput {
	return models.IndexBookInput{
		Metadata: models.BookMetadata{
			BookID:          bookID,
			Title:           fmt.Sprintf("Book %d", bookID),
			Author:          author,
			Language:        language,
			PublicationYear: year,
			ReleaseYear:     1994,
		},
		Tokens: tokens,
	}
}
