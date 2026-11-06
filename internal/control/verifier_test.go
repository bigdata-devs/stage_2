package control

import (
	"context"
	"errors"
	"slices"
	"testing"

	"boogle-engine/internal/models"
)

type fakeIndexedBooks []models.BookID

func (books fakeIndexedBooks) IndexedIDs() []models.BookID {
	return slices.Clone(books)
}

type fakeSearch struct {
	missing map[models.BookID]bool
	failOn  models.BookID
	failure error
	checked []models.BookID
}

func (search *fakeSearch) IsPublished(_ context.Context, bookID models.BookID) (bool, error) {
	search.checked = append(search.checked, bookID)
	if bookID == search.failOn {
		return false, search.failure
	}
	return !search.missing[bookID], nil
}

func newTestVerifier(t *testing.T, indexed fakeIndexedBooks, search *fakeSearch) Verifier {
	t.Helper()
	verifier, err := NewVerifier(indexed, search)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier
}

func TestVerifyWithNothingIndexedChecksNothing(t *testing.T) {
	search := &fakeSearch{}

	report, err := newTestVerifier(t, fakeIndexedBooks{}, search).Verify(context.Background())

	if err != nil || report.Indexed != 0 || len(report.Missing) != 0 {
		t.Fatalf("Verify = %+v, %v; want an empty report", report, err)
	}
	if len(search.checked) != 0 {
		t.Fatalf("search checked %v, want nothing", search.checked)
	}
}

func TestVerifyPassesWhenEveryIndexedBookIsPublished(t *testing.T) {
	search := &fakeSearch{}

	report, err := newTestVerifier(t, fakeIndexedBooks{11, 84, 1342}, search).Verify(context.Background())

	if err != nil || report.Indexed != 3 || len(report.Missing) != 0 {
		t.Fatalf("Verify = %+v, %v; want 3 indexed books and none missing", report, err)
	}
	if !slices.Equal(search.checked, []models.BookID{11, 84, 1342}) {
		t.Fatalf("search checked %v, want every indexed book", search.checked)
	}
}

func TestVerifyReportsTheIndexedBooksMissingFromSearch(t *testing.T) {
	search := &fakeSearch{missing: map[models.BookID]bool{84: true}}

	report, err := newTestVerifier(t, fakeIndexedBooks{11, 84, 1342}, search).Verify(context.Background())

	if !errors.Is(err, ErrBooksMissing) {
		t.Fatalf("Verify error = %v, want ErrBooksMissing", err)
	}
	if want := "verify: 1 of 3 indexed books are missing from the datamart"; err.Error() != want {
		t.Errorf("Verify error = %q, want %q", err.Error(), want)
	}
	if !slices.Equal(report.Missing, []models.BookID{84}) || len(search.checked) != 3 {
		t.Errorf("report = %+v after %d checks, want book 84 missing after 3 checks", report, len(search.checked))
	}
}

func TestVerifyStopsAtTheFirstSearchFailure(t *testing.T) {
	failure := errors.New("search service unreachable")
	search := &fakeSearch{failOn: 84, failure: failure}

	_, err := newTestVerifier(t, fakeIndexedBooks{11, 84, 1342}, search).Verify(context.Background())

	if !errors.Is(err, failure) || errors.Is(err, ErrBooksMissing) {
		t.Fatalf("Verify error = %v, want the search failure only", err)
	}
	if !slices.Equal(search.checked, []models.BookID{11, 84}) {
		t.Fatalf("search checked %v, want to stop at book 84", search.checked)
	}
}

func TestVerifyStopsWhenInterrupted(t *testing.T) {
	search := &fakeSearch{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestVerifier(t, fakeIndexedBooks{11, 84}, search).Verify(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify error = %v, want context.Canceled", err)
	}
	if len(search.checked) != 0 {
		t.Fatalf("search checked %v after cancellation, want nothing", search.checked)
	}
}

func TestNewVerifierRejectsMissingDependencies(t *testing.T) {
	if _, err := NewVerifier(nil, &fakeSearch{}); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("NewVerifier without indexed books = %v, want ErrMissingDependency", err)
	}
	if _, err := NewVerifier(fakeIndexedBooks{}, nil); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("NewVerifier without search service = %v, want ErrMissingDependency", err)
	}
}
