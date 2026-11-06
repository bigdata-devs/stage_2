package mongodb

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const (
	InvertedIndexCollection = "inverted_index"
	BooksCollection         = "books"
)

const rebuildSuffix = "_rebuild"

const (
	StatusPending   = "pending"
	StatusPublished = "published"
)

var bookFilterIndexes = []mongo.IndexModel{
	{Keys: bson.D{
		{Key: "author", Value: 1},
		{Key: "language", Value: 1},
		{Key: "publication_year", Value: 1},
	}},
	{Keys: bson.D{{Key: "language", Value: 1}}},
	{Keys: bson.D{{Key: "publication_year", Value: 1}}},
}

var postingIndexes = []mongo.IndexModel{
	{Keys: bson.D{{Key: "postings.book_id", Value: 1}}},
}

func (client Client) EnsureIndexes(ctx context.Context) error {
	created, err := createBookIndexes(ctx, client.database.Collection(BooksCollection))
	if err != nil {
		return fmt.Errorf("ensure book indexes: %w", err)
	}
	postingIndexesCreated, err := createPostingIndexes(ctx, client.database.Collection(InvertedIndexCollection))
	if err != nil {
		return fmt.Errorf("ensure posting indexes: %w", err)
	}
	slog.Info("mongodb indexes ready", "created", created, "posting_indexes_created", postingIndexesCreated)
	return nil
}

func createBookIndexes(ctx context.Context, books *mongo.Collection) ([]string, error) {
	return books.Indexes().CreateMany(ctx, bookFilterIndexes)
}

func createPostingIndexes(ctx context.Context, postings *mongo.Collection) ([]string, error) {
	return postings.Indexes().CreateMany(ctx, postingIndexes)
}
