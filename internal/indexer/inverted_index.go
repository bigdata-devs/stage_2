package indexer

import (
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

// InvertedIndex maps every term to the ascending, duplicate-free list of books containing it.
// It is the in-memory port of the Stage 1 postings structure and is safe for concurrent use:
// many indexing goroutines may add books while search goroutines read postings.
type InvertedIndex struct {
	mutex    sync.RWMutex
	postings map[string][]models.BookID
}

// NewInvertedIndex returns an empty index.
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{postings: map[string][]models.BookID{}}
}

// AddBook adds a book under each of its distinct terms. Adding the same book again leaves the
// index unchanged, so re-indexing is idempotent.
func (index *InvertedIndex) AddBook(bookID models.BookID, tokens []string) {
	terms := distinctTerms(tokens)
	index.mutex.Lock()
	defer index.mutex.Unlock()
	for _, term := range terms {
		index.postings[term] = insertSorted(index.postings[term], bookID)
	}
}

// Postings returns a copy of the books containing term; an unknown term yields an empty list.
func (index *InvertedIndex) Postings(term string) []models.BookID {
	index.mutex.RLock()
	defer index.mutex.RUnlock()
	return append([]models.BookID{}, index.postings[term]...)
}

// TermCount returns the number of distinct terms in the index.
func (index *InvertedIndex) TermCount() int {
	index.mutex.RLock()
	defer index.mutex.RUnlock()
	return len(index.postings)
}

// distinctTerms is computed before taking the lock, so the critical section stays short.
func distinctTerms(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	terms := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, found := seen[token]; !found {
			seen[token] = struct{}{}
			terms = append(terms, token)
		}
	}
	return terms
}

// insertSorted inserts bookID into an ascending postings list unless it is already present.
func insertSorted(postings []models.BookID, bookID models.BookID) []models.BookID {
	position, found := slices.BinarySearch(postings, bookID)
	if found {
		return postings
	}
	return slices.Insert(postings, position, bookID)
}
