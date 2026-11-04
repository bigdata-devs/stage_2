package mongodb

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"boogle-engine/internal/models"
)

type DatamartReader struct {
	client Client
}

func NewDatamartReader(client Client) DatamartReader {
	return DatamartReader{client: client}
}

func (reader DatamartReader) PostingsForTerms(ctx context.Context, terms []string) ([]models.TermPostings, error) {
	if len(terms) == 0 {
		return []models.TermPostings{}, nil
	}

	var documents []termDocument
	cursor, err := reader.index().Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: terms}}}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find postings for %d terms: %w", len(terms), err)
	}
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, fmt.Errorf("decode postings for %d terms: %w", len(terms), err)
	}

	postings := make([]models.TermPostings, 0, len(documents))
	for _, document := range documents {
		postings = append(postings, models.TermPostings{Term: document.Term, Postings: postingsOf(document.Postings)})
	}
	return postings, nil
}

func (reader DatamartReader) BooksByID(ctx context.Context, bookIDs []models.BookID) ([]models.BookMetadata, error) {
	if len(bookIDs) == 0 {
		return []models.BookMetadata{}, nil
	}

	var documents []bookDocument
	cursor, err := reader.books().Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: bookIDs}}}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find %d books: %w", len(bookIDs), err)
	}
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, fmt.Errorf("decode %d books: %w", len(bookIDs), err)
	}
	return metadataOf(documents), nil
}

type termDocument struct {
	Term     string            `bson:"_id"`
	Postings []postingDocument `bson:"postings"`
}

type postingDocument struct {
	BookID models.BookID `bson:"book_id"`
	TF     int           `bson:"tf"`
}

func postingsOf(documents []postingDocument) []models.Posting {
	postings := make([]models.Posting, 0, len(documents))
	for _, document := range documents {
		postings = append(postings, models.Posting{BookID: document.BookID, TF: document.TF})
	}
	return postings
}

func (reader DatamartReader) books() *mongo.Collection {
	return reader.client.database.Collection(BooksCollection)
}

func (reader DatamartReader) index() *mongo.Collection {
	return reader.client.database.Collection(InvertedIndexCollection)
}
