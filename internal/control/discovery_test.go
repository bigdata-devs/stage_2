package control

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/models"
)

type fakeDatalake struct {
	books []models.BookID
	err   error
}

func (datalake fakeDatalake) ListDatalakeBooks(context.Context) ([]models.BookID, error) {
	return datalake.books, datalake.err
}

func newTestDiscoverer(t *testing.T, datalake fakeDatalake) Discoverer {
	t.Helper()
	discoverer, err := NewDiscoverer(datalake)
	if err != nil {
		t.Fatalf("NewDiscoverer: %v", err)
	}
	return discoverer
}

func TestDiscoverAppendsTheDatalakeBooksToTheCandidates(t *testing.T) {
	backing := make([]models.BookID, 1, 4)
	backing[0] = 1342

	discovered, err := newTestDiscoverer(t, fakeDatalake{books: []models.BookID{5, 17}}).Discover(context.Background(), backing)

	if err != nil || !slices.Equal(discovered, []models.BookID{1342, 5, 17}) {
		t.Fatalf("Discover = %v, %v; want [1342 5 17]", discovered, err)
	}
	if spare := backing[:3]; spare[1] != 0 || spare[2] != 0 {
		t.Errorf("Discover wrote into the caller's slice: %v", spare)
	}
}

func TestDiscoverWithAnEmptyDatalakeKeepsTheCandidates(t *testing.T) {
	discovered, err := newTestDiscoverer(t, fakeDatalake{}).Discover(context.Background(), []models.BookID{84})

	if err != nil || !slices.Equal(discovered, []models.BookID{84}) {
		t.Fatalf("Discover = %v, %v; want [84]", discovered, err)
	}
}

func TestDiscoverReportsAnUnreachableIngestionService(t *testing.T) {
	failure := errors.New("connection refused")

	_, err := newTestDiscoverer(t, fakeDatalake{err: failure}).Discover(context.Background(), nil)

	if !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), "discover pending books: ") {
		t.Fatalf("Discover error = %v, want the listing failure", err)
	}
}

func TestNewDiscovererRejectsAMissingLister(t *testing.T) {
	if _, err := NewDiscoverer(nil); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("NewDiscoverer(nil) = %v, want ErrMissingDependency", err)
	}
}
