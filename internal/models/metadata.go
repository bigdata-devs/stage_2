package models

// BookMetadata holds the normalized header fields of a book that search results and filters use.
type BookMetadata struct {
	BookID   BookID `json:"book_id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Language string `json:"language"`
}
