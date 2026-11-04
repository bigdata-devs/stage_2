package mongodb

import (
	"context"
	"testing"

	"boogle-engine/internal/models"
)

func TestPostingsForTermsReturnsMatchingTermsOrderedByTerm(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)

	if err := indexer.IndexBook(context.Background(), sampleInput(7, "island", "ship", "island")); err != nil {
		t.Fatalf("book 7: unexpected error: %v", err)
	}
	if err := indexer.IndexBook(context.Background(), sampleInput(3, "island")); err != nil {
		t.Fatalf("book 3: unexpected error: %v", err)
	}

	got, err := NewDatamartReader(client).PostingsForTerms(context.Background(),
		[]string{"ship", "unknown", "island"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTermPostings(t, got, []models.TermPostings{
		{Term: "island", Postings: []models.Posting{{BookID: 3, TF: 1}, {BookID: 7, TF: 2}}},
		{Term: "ship", Postings: []models.Posting{{BookID: 7, TF: 1}}},
	})
}

func TestPostingsForTermsWithOnlyUnknownTermsReturnsEmptyList(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	if err := NewBookIndexer(client).IndexBook(context.Background(), sampleInput(1, "island")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := NewDatamartReader(client).PostingsForTerms(context.Background(), []string{"ghost"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTermPostings(t, got, []models.TermPostings{})
}

func TestPostingsForTermsWithoutTermsReturnsEmptyList(t *testing.T) {
	client := liveClient(t)

	got, err := NewDatamartReader(client).PostingsForTerms(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTermPostings(t, got, []models.TermPostings{})
}

func TestBooksByIDReturnsMetadataOrderedByBookID(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	indexer := NewBookIndexer(client)
	for _, bookID := range []models.BookID{9, 3, 5} {
		if err := indexer.IndexBook(context.Background(), sampleInput(bookID, "shore")); err != nil {
			t.Fatalf("book %d: unexpected error: %v", bookID, err)
		}
	}

	got, err := NewDatamartReader(client).BooksByID(context.Background(), []models.BookID{9, 5, 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertBookIDs(t, got, []models.BookID{5, 9})
	if got[0].Title != "Title of Book" || got[0].Author != "An Author" || got[0].PublicationYear != 1719 {
		t.Fatalf("unexpected metadata for book 5: %+v", got[0])
	}
}

func TestBooksWithoutMatchesReturnsEmptyList(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)

	got, err := NewDatamartReader(client).BooksByID(context.Background(), []models.BookID{1, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertBookIDs(t, got, []models.BookID{})
}

func TestBooksWithoutIDsReturnsEmptyList(t *testing.T) {
	client := liveClient(t)

	got, err := NewDatamartReader(client).BooksByID(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertBookIDs(t, got, []models.BookID{})
}

func assertTermPostings(t *testing.T, got []models.TermPostings, want []models.TermPostings) {
	t.Helper()
	if got == nil {
		t.Fatal("expected a term postings list, got nil")
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d term postings, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i].Term != want[i].Term {
			t.Fatalf("entry %d: expected term %q, got %q", i, want[i].Term, got[i].Term)
		}
		assertPostings(t, got[i].Postings, want[i].Postings)
	}
}

func assertBookIDs(t *testing.T, got []models.BookMetadata, want []models.BookID) {
	t.Helper()
	if got == nil {
		t.Fatal("expected a book list, got nil")
	}
	if len(got) != len(want) {
		t.Fatalf("expected books %v, got %d results: %+v", want, len(got), got)
	}
	for i := range want {
		if got[i].BookID != want[i] {
			t.Fatalf("result %d: expected book %d, got %d", i, want[i], got[i].BookID)
		}
	}
}
