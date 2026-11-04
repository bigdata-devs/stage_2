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

func TestSearchBooksRanksByTermFrequency(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: []string{"adventure"}, Limit: 10})
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

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: []string{"sea"}, Limit: 10})
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
			SearchRequest{Terms: []string{"adventure"}, Offset: page.offset, Limit: page.limit})
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

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: []string{"castaway"}, Limit: 10})
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
			SearchRequest{Terms: []string{"adventure"}, Language: testCase.language, Limit: 10})
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
			SearchRequest{Terms: []string{"adventure"}, Author: testCase.author, Limit: 10})
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
			SearchRequest{Terms: []string{"adventure"}, Year: testCase.year, Limit: 10})
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
			request: SearchRequest{Terms: []string{"adventure"}, Author: "ane", Language: "fr", Year: 1850, Limit: 10},
			want:    []models.BookID{2},
		},
		{
			name:    "year keeps the french book out",
			request: SearchRequest{Terms: []string{"adventure"}, Author: "ane", Language: "en", Year: 1813, Limit: 10},
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

func TestSearchBooksRequiresEveryTerm(t *testing.T) {
	reader := seededReader(t)
	cases := []struct {
		name      string
		terms     []string
		wantTotal int
		wantIDs   []models.BookID
	}{
		{name: "two terms shared by one book", terms: []string{"adventure", "island"}, wantTotal: 1, wantIDs: []models.BookID{1}},
		{name: "term order does not matter", terms: []string{"island", "adventure"}, wantTotal: 1, wantIDs: []models.BookID{1}},
		{name: "a term only another book has", terms: []string{"whale", "sea"}, wantTotal: 1, wantIDs: []models.BookID{4}},
		{name: "three terms", terms: []string{"adventure", "island", "sea"}, wantTotal: 1, wantIDs: []models.BookID{1}},
		{name: "terms no single book shares", terms: []string{"adventure", "whale"}, wantTotal: 0, wantIDs: []models.BookID{}},
		{name: "a term the index lacks", terms: []string{"adventure", "zzzz"}, wantTotal: 0, wantIDs: []models.BookID{}},
		{name: "repeated term counts once", terms: []string{"adventure", "adventure"}, wantTotal: 2, wantIDs: []models.BookID{2, 1}},
		{name: "pending book stays hidden", terms: []string{"adventure", "castaway"}, wantTotal: 0, wantIDs: []models.BookID{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: testCase.terms, Limit: 10})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Total != testCase.wantTotal {
				t.Fatalf("expected total %d, got %d", testCase.wantTotal, result.Total)
			}
			assertBookIDs(t, result.Books, testCase.wantIDs)
		})
	}
}

func TestSearchBooksRanksMultiTermMatchesBySummedFrequency(t *testing.T) {
	reader := seededReader(t)
	indexer := NewBookIndexer(reader.client)
	for _, input := range []models.IndexBookInput{
		metadataInput(5, "Jules Verne", "fr", 1870, "storm", "storm", "storm", "storm", "ship"),
		metadataInput(6, "Jules Verne", "fr", 1872, "storm", "ship", "ship", "ship", "ship", "ship"),
		metadataInput(7, "Jules Verne", "fr", 1874, "storm", "ship", "ship", "ship", "ship", "ship"),
	} {
		if err := indexer.IndexBook(context.Background(), input); err != nil {
			t.Fatalf("book %d: unexpected error: %v", input.Metadata.BookID, err)
		}
	}

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: []string{"storm", "ship"}, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected total 3, got %d", result.Total)
	}
	assertBookIDs(t, result.Books, []models.BookID{6, 7, 5})
}

func TestSearchBooksWithUnknownTermReturnsEmptyList(t *testing.T) {
	reader := seededReader(t)

	result, err := reader.SearchBooks(context.Background(), SearchRequest{Terms: []string{"zzzz"}, Limit: 10})
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
		{name: "no terms", request: SearchRequest{Limit: 10}},
		{name: "empty term among terms", request: SearchRequest{Terms: []string{"adventure", ""}, Limit: 10}},
		{name: "negative offset", request: SearchRequest{Terms: []string{"adventure"}, Offset: -1, Limit: 10}},
		{name: "zero limit", request: SearchRequest{Terms: []string{"adventure"}}},
		{name: "negative year", request: SearchRequest{Terms: []string{"adventure"}, Year: -1, Limit: 10}},
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
