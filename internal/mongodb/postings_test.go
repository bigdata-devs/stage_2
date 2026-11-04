package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"boogle-engine/internal/models"
)

type postingDocument struct {
	BookID models.BookID `bson:"book_id"`
	TF     int           `bson:"tf"`
}

type termDocument struct {
	Term     string            `bson:"_id"`
	Postings []postingDocument `bson:"postings"`
}

func postingsForTerms(ctx context.Context, reader DatamartReader, terms []string) ([]termDocument, error) {
	var documents []termDocument
	cursor, err := reader.index().Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: terms}}}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, err
	}
	return documents, nil
}
