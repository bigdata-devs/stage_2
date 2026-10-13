package models

// BookMetadata holds the normalized header fields of a book that search results and filters use.
// PublicationYear is the original publication year and ReleaseYear the Gutenberg release year;
// both are 0 when the header does not provide them, and search filters ignore such books.
type BookMetadata struct {
	BookID          BookID `json:"book_id"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	Language        string `json:"language"`
	PublicationYear int    `json:"publication_year"`
	ReleaseYear     int    `json:"release_year"`
}
