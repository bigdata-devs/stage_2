package mongodb

import "boogle-engine/internal/models"

type bookDocument struct {
	models.BookMetadata `bson:",inline"`
	Status              string `bson:"status"`
}

func newBookDocument(metadata models.BookMetadata, status string) bookDocument {
	return bookDocument{BookMetadata: metadata, Status: status}
}
