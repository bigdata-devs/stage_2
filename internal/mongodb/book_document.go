package mongodb

import "boogle-engine/internal/models"

// bookDocument is the wire shape of one metadata record: the normalized header fields search
// filters on, inlined from models.BookMetadata, plus the publication status that gates visibility.
// BookID is stored as _id, so a book's identity doubles as its uniqueness constraint.
type bookDocument struct {
	models.BookMetadata `bson:",inline"`
	Status              string `bson:"status"`
}

func newBookDocument(metadata models.BookMetadata, status string) bookDocument {
	return bookDocument{BookMetadata: metadata, Status: status}
}
