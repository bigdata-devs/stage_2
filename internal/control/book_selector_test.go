package control

import (
	"sync"
	"testing"

	"boogle-engine/internal/models"
)

func TestClaimHandsOutEveryCandidateOnce(t *testing.T) {
	selector := NewBookSelector([]models.BookID{1342, 84, 11})
	claimed := []models.BookID{}
	for {
		bookID, ok := selector.Claim()
		if !ok {
			break
		}
		claimed = append(claimed, bookID)
	}
	if len(claimed) != 3 || claimed[0] != 1342 || claimed[1] != 84 || claimed[2] != 11 {
		t.Fatalf("expected candidates in order [1342 84 11], got %v", claimed)
	}
	if _, ok := selector.Claim(); ok {
		t.Fatal("expected exhausted selector to claim nothing")
	}
}

func TestNewBookSelectorDropsRepeats(t *testing.T) {
	selector := NewBookSelector([]models.BookID{1, 2, 1, 3, 2})
	count := 0
	for {
		if _, ok := selector.Claim(); !ok {
			break
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 unique candidates, got %d", count)
	}
}

func TestConcurrentClaimsNeverRepeatCandidates(t *testing.T) {
	candidates := make([]models.BookID, 100)
	for index := range candidates {
		candidates[index] = models.BookID(index + 1)
	}
	selector := NewBookSelector(candidates)

	const workers = 8
	var mutex sync.Mutex
	seen := map[models.BookID]int{}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				bookID, ok := selector.Claim()
				if !ok {
					return
				}
				mutex.Lock()
				seen[bookID]++
				mutex.Unlock()
			}
		}()
	}
	group.Wait()

	if len(seen) != len(candidates) {
		t.Fatalf("expected %d distinct candidates, got %d", len(candidates), len(seen))
	}
	for bookID, times := range seen {
		if times != 1 {
			t.Errorf("candidate %d claimed %d times", bookID, times)
		}
	}
}
