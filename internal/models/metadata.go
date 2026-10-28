package models

// BookMetadata holds the normalized header fields of a book that search results and filters use.
// PublicationYear is the original publication year, falling back to the Gutenberg release year
// when the header omits it; ReleaseYear is always the Gutenberg release year. Both are 0 when the
// header does not provide them, and search filters ignore such books. The bson field names match
// the metadata datamart indexes (author, language, publication_year).
type BookMetadata struct {
	BookID          BookID `json:"book_id" bson:"_id"`
	Title           string `json:"title" bson:"title"`
	Author          string `json:"author" bson:"author"`
	Language        string `json:"language" bson:"language"`
	PublicationYear int    `json:"publication_year" bson:"publication_year"`
	ReleaseYear     int    `json:"release_year" bson:"release_year"`
}
