package indexer

import (
	"slices"
	"sync"

	"boogle-engine/internal/models"
)

type InvertedIndex struct {
	mutex    sync.RWMutex
	postings map[string][]models.BookID
}

func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{postings: map[string][]models.BookID{}}
}

func (index *InvertedIndex) AddBook(bookID models.BookID, tokens []string) {
	terms := distinctTerms(tokens)
	index.mutex.Lock()
	defer index.mutex.Unlock()
	for _, term := range terms {
		index.postings[term] = insertSorted(index.postings[term], bookID)
	}
}

func (index *InvertedIndex) Postings(term string) []models.BookID {
	index.mutex.RLock()
	defer index.mutex.RUnlock()
	return append([]models.BookID{}, index.postings[term]...)
}

func (index *InvertedIndex) TermCount() int {
	index.mutex.RLock()
	defer index.mutex.RUnlock()
	return len(index.postings)
}

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

func insertSorted(postings []models.BookID, bookID models.BookID) []models.BookID {
	position, found := slices.BinarySearch(postings, bookID)
	if found {
		return postings
	}
	return slices.Insert(postings, position, bookID)
}
