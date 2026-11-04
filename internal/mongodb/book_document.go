package mongodb

import "boogle-engine/internal/models"

type bookDocument struct {
	BookID          models.BookID `bson:"_id"`
	Title           string        `bson:"title"`
	Author          string        `bson:"author"`
	Language        string        `bson:"language"`
	PublicationYear int           `bson:"publication_year"`
	ReleaseYear     int           `bson:"release_year"`
	Status          string        `bson:"status"`
}

func newBookDocument(metadata models.BookMetadata, status string) bookDocument {
	return bookDocument{
		BookID:          metadata.BookID,
		Title:           metadata.Title,
		Author:          metadata.Author,
		Language:        metadata.Language,
		PublicationYear: metadata.PublicationYear,
		ReleaseYear:     metadata.ReleaseYear,
		Status:          status,
	}
}

func (document bookDocument) metadata() models.BookMetadata {
	return models.BookMetadata{
		BookID:          document.BookID,
		Title:           document.Title,
		Author:          document.Author,
		Language:        document.Language,
		PublicationYear: document.PublicationYear,
		ReleaseYear:     document.ReleaseYear,
	}
}

func metadataOf(documents []bookDocument) []models.BookMetadata {
	books := make([]models.BookMetadata, 0, len(documents))
	for _, document := range documents {
		books = append(books, document.metadata())
	}
	return books
}
