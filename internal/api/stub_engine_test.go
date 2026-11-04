package api

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"boogle-engine/internal/models"
)

type StubEngine struct {
	catalog []models.BookMetadata
}

func NewStubEngine() StubEngine {
	catalog := sampleCatalog()
	slices.SortFunc(catalog, func(first, second models.BookMetadata) int {
		return cmp.Compare(first.BookID, second.BookID)
	})
	return StubEngine{catalog: catalog}
}

func (engine StubEngine) Search(_ context.Context, query SearchQuery) (SearchPage, error) {
	matches := engine.matchingBooks(query)
	return SearchPage{Total: len(matches), Books: pageOf(matches, query.Offset, query.Limit)}, nil
}

func (engine StubEngine) matchingBooks(query SearchQuery) []models.BookMetadata {
	matches := []models.BookMetadata{}
	for _, book := range engine.catalog {
		if titleContainsEveryTerm(book.Title, query.Terms) && matchesFilters(book, query.Filters) {
			matches = append(matches, book)
		}
	}
	return matches
}

func titleContainsEveryTerm(title string, terms []string) bool {
	for _, term := range terms {
		if !containsFold(title, term) {
			return false
		}
	}
	return true
}

func matchesFilters(book models.BookMetadata, filters SearchFilters) bool {
	return matchesAuthor(book, filters.Author) &&
		matchesLanguage(book, filters.Language) &&
		matchesYear(book, filters.Year)
}

func matchesAuthor(book models.BookMetadata, author string) bool {
	return author == "" || containsFold(book.Author, author)
}

func matchesLanguage(book models.BookMetadata, language string) bool {
	if language == "" {
		return true
	}
	return slices.ContainsFunc(strings.Split(book.Language, ","), func(code string) bool {
		return strings.EqualFold(strings.TrimSpace(code), language)
	})
}

func matchesYear(book models.BookMetadata, year int) bool {
	return year == 0 || book.PublicationYear == year
}

func containsFold(text, fragment string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(fragment))
}

func pageOf(books []models.BookMetadata, offset, limit int) []models.BookMetadata {
	if offset >= len(books) {
		return []models.BookMetadata{}
	}
	pageSize := min(limit, len(books)-offset)
	return books[offset : offset+pageSize]
}

func sampleCatalog() []models.BookMetadata {
	return []models.BookMetadata{
		{BookID: 11, Title: "Alice's Adventures in Wonderland", Author: "Lewis Carroll", Language: "en", PublicationYear: 1865},
		{BookID: 74, Title: "The Adventures of Tom Sawyer", Author: "Mark Twain", Language: "en", PublicationYear: 1876},
		{BookID: 76, Title: "Adventures of Huckleberry Finn", Author: "Mark Twain", Language: "en", PublicationYear: 1884},
		{BookID: 84, Title: "Frankenstein; Or, The Modern Prometheus", Author: "Mary Wollstonecraft Shelley", Language: "en", PublicationYear: 1818},
		{BookID: 120, Title: "Treasure Island", Author: "Robert Louis Stevenson", Language: "en", PublicationYear: 1883},
		{BookID: 521, Title: "The Life and Adventures of Robinson Crusoe", Author: "Daniel Defoe", Language: "en", PublicationYear: 1719},
		{BookID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen", Language: "en", PublicationYear: 1813},
		{BookID: 1661, Title: "The Adventures of Sherlock Holmes", Author: "Arthur Conan Doyle", Language: "en", PublicationYear: 1892},
		{BookID: 2000, Title: "Don Quijote", Author: "Miguel de Cervantes Saavedra", Language: "es", PublicationYear: 1605},
		{BookID: 2701, Title: "Moby Dick; Or, The Whale", Author: "Herman Melville", Language: "en", PublicationYear: 1851},
		{BookID: 17989, Title: "Le comte de Monte-Cristo, Tome I", Author: "Alexandre Dumas", Language: "fr", PublicationYear: 1844},
		{BookID: 22367, Title: "Die Verwandlung", Author: "Franz Kafka", Language: "de", PublicationYear: 1915},
	}
}
