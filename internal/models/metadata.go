package models

type BookMetadata struct {
	BookID          BookID `json:"book_id" bson:"_id"`
	Title           string `json:"title" bson:"title"`
	Author          string `json:"author" bson:"author"`
	Language        string `json:"language" bson:"language"`
	PublicationYear int    `json:"publication_year" bson:"publication_year"`
	ReleaseYear     int    `json:"release_year" bson:"release_year"`
}
