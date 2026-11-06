package mongodb

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"boogle-engine/internal/models"
)

const postingUpsertBatchSize = 1000

type BookIndexer struct {
	client Client
}

func NewBookIndexer(client Client) BookIndexer {
	return BookIndexer{client: client}
}

func (indexer BookIndexer) IndexBook(ctx context.Context, input models.IndexBookInput) error {
	bookID := input.Metadata.BookID
	if err := writeMetadata(ctx, indexer.books(), input.Metadata, StatusPending); err != nil {
		return fmt.Errorf("mark book %d pending: %w", bookID, err)
	}
	if err := removeBookPostings(ctx, indexer.index(), bookID); err != nil {
		return fmt.Errorf("drop previous postings of book %d: %w", bookID, err)
	}
	if err := upsertPostings(ctx, indexer.index(), bookID, termFrequencies(input.Tokens)); err != nil {
		return fmt.Errorf("upsert postings for book %d: %w", bookID, err)
	}
	if err := publishMetadata(ctx, indexer.books(), bookID); err != nil {
		return fmt.Errorf("publish book %d: %w", bookID, err)
	}
	return nil
}

func (indexer BookIndexer) books() *mongo.Collection {
	return indexer.client.database.Collection(BooksCollection)
}

func (indexer BookIndexer) index() *mongo.Collection {
	return indexer.client.database.Collection(InvertedIndexCollection)
}

func writeMetadata(ctx context.Context, collection *mongo.Collection, metadata models.BookMetadata, status string) error {
	_, err := collection.ReplaceOne(ctx,
		bson.D{{Key: "_id", Value: metadata.BookID}},
		newBookDocument(metadata, status),
		options.Replace().SetUpsert(true))
	return err
}

func publishMetadata(ctx context.Context, collection *mongo.Collection, bookID models.BookID) error {
	_, err := collection.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: bookID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: StatusPublished}}}})
	return err
}

func removeBookPostings(ctx context.Context, collection *mongo.Collection, bookID models.BookID) error {
	_, err := collection.UpdateMany(ctx,
		bson.D{{Key: "postings.book_id", Value: bookID}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "postings", Value: bson.D{{Key: "book_id", Value: bookID}}}}}})
	return err
}

func upsertPostings(ctx context.Context, collection *mongo.Collection, bookID models.BookID, frequencies map[string]int) error {
	if len(frequencies) == 0 {
		return nil
	}
	terms := make([]string, 0, len(frequencies))
	for term := range frequencies {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	for start := 0; start < len(terms); start += postingUpsertBatchSize {
		end := min(start+postingUpsertBatchSize, len(terms))
		operations := make([]mongo.WriteModel, 0, end-start)
		for _, term := range terms[start:end] {
			operations = append(operations, postingUpsert(term, bookID, frequencies[term]))
		}
		if _, err := collection.BulkWrite(ctx, operations, options.BulkWrite().SetOrdered(false)); err != nil {
			return err
		}
	}
	return nil
}

func postingUpsert(term string, bookID models.BookID, termFrequency int) *mongo.UpdateOneModel {
	fresh := bson.D{{Key: "book_id", Value: bookID}, {Key: "tf", Value: termFrequency}}
	setUnion := bson.D{{Key: "$setUnion", Value: bson.A{postingsWithout(bookID), bson.A{fresh}}}}
	sortArray := bson.D{{Key: "$sortArray", Value: bson.D{
		{Key: "input", Value: setUnion},
		{Key: "sortBy", Value: bson.D{{Key: "book_id", Value: 1}}},
	}}}
	setPostings := bson.D{{Key: "$set", Value: bson.D{{Key: "postings", Value: sortArray}}}}

	return mongo.NewUpdateOneModel().
		SetFilter(bson.D{{Key: "_id", Value: term}}).
		SetUpdate(bson.A{setPostings}).
		SetUpsert(true)
}

func postingsWithout(bookID models.BookID) bson.D {
	stored := bson.D{{Key: "$ifNull", Value: bson.A{"$postings", bson.A{}}}}
	dropBook := bson.D{{Key: "$ne", Value: bson.A{"$$this.book_id", bookID}}}
	return bson.D{{Key: "$filter", Value: bson.D{
		{Key: "input", Value: stored},
		{Key: "cond", Value: dropBook},
	}}}
}

func termFrequencies(tokens []string) map[string]int {
	frequencies := make(map[string]int, len(tokens))
	for _, token := range tokens {
		frequencies[token]++
	}
	return frequencies
}
