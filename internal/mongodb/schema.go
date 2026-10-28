package mongodb

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Names of the two datamart collections, both inside the configured database.
const (
	// InvertedIndexCollection holds one document per term: the term itself as the _id and its
	// posting list. Keying by term gives duplicate-free, O(1)-routed writes for free.
	InvertedIndexCollection = "inverted_index"
	// BooksCollection holds one document per book: its metadata plus the publication status that
	// tells the Search Service whether the book may appear in results yet.
	BooksCollection = "books"
)

// rebuildSuffix names the temporary collections a rebuild fills before swapping them over the live
// ones, so search keeps reading the previous complete index until the new one is ready.
const rebuildSuffix = "_rebuild"

// Publication states of a metadata document. A book only becomes searchable once its status is
// StatusPublished, which is how a partially indexed book stays invisible to readers.
const (
	// StatusPending marks a book whose metadata was written but whose postings are not stored yet.
	StatusPending = "pending"
	// StatusPublished marks a fully indexed book that the Search Service may return.
	StatusPublished = "published"
)

// bookFilterIndexes are the indexes that back the Search Service metadata filters. The compound
// index answers "author only" and "author + language + year" (a prefix serves the shorter
// filter), while the single-field indexes cover "language only" and "year only" queries, which
// the compound prefix cannot. They are created on start-up and never dropped.
var bookFilterIndexes = []mongo.IndexModel{
	{Keys: bson.D{
		{Key: "author", Value: 1},
		{Key: "language", Value: 1},
		{Key: "publication_year", Value: 1},
	}},
	{Keys: bson.D{{Key: "language", Value: 1}}},
	{Keys: bson.D{{Key: "publication_year", Value: 1}}},
}

// postingIndexes back the write path's removal of a book's previous postings, which filters the
// multikey postings.book_id field. They are created on start-up and on every rebuild's temporary
// collection, and travel with the collection when the rebuild swaps it into place.
var postingIndexes = []mongo.IndexModel{
	{Keys: bson.D{{Key: "postings.book_id", Value: 1}}},
}

// EnsureIndexes creates every index the datamart queries rely on. It is idempotent: MongoDB
// keeps an index whose name and key already exist, so services call it at each start-up.
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

// createBookIndexes creates the query indexes on the given books collection and reports the names
// MongoDB assigned. A rebuild runs it on its temporary collection before swapping it into place.
func createBookIndexes(ctx context.Context, books *mongo.Collection) ([]string, error) {
	return books.Indexes().CreateMany(ctx, bookFilterIndexes)
}

// createPostingIndexes creates the write-path indexes on the given inverted index collection.
func createPostingIndexes(ctx context.Context, postings *mongo.Collection) ([]string, error) {
	return postings.Indexes().CreateMany(ctx, postingIndexes)
}
