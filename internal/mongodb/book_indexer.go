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

// postingUpsertBatchSize caps how many term updates one bulk request carries, so a single book's
// postings never push a request past the server's practical message-size limit.
const postingUpsertBatchSize = 1000

// BookIndexer writes books into the datamart: their metadata into the books collection and their
// body terms into the inverted index. Writes are idempotent, so replaying the same book never
// duplicates postings, and a book only becomes visible to search once its postings are complete.
type BookIndexer struct {
	client Client
}

// NewBookIndexer wires the writer to a connected datamart.
func NewBookIndexer(client Client) BookIndexer {
	return BookIndexer{client: client}
}

// IndexBook stores one book in four ordered steps: it marks the metadata pending, drops the
// postings of the previous version, upserts every current term posting, and finally publishes the
// metadata. Search only returns published books, so a crash at any step leaves the book invisible
// rather than half-indexed, and a retry converges to the same state.
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

// writeMetadata stores a book's header fields and publication status, replacing any record already
// held under that book ID.
func writeMetadata(ctx context.Context, collection *mongo.Collection, metadata models.BookMetadata, status string) error {
	_, err := collection.ReplaceOne(ctx,
		bson.D{{Key: "_id", Value: metadata.BookID}},
		newBookDocument(metadata, status),
		options.Replace().SetUpsert(true))
	return err
}

// publishMetadata flips a book's status to published, which is the moment search may return it.
func publishMetadata(ctx context.Context, collection *mongo.Collection, bookID models.BookID) error {
	_, err := collection.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: bookID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: StatusPublished}}}})
	return err
}

// removeBookPostings drops a book's entry from every term it was recorded under, so a re-index
// forgets terms the new version no longer contains. It is bounded by the multikey index on
// postings.book_id. Term documents left with an empty posting list never match a search and the
// next rebuild discards them.
func removeBookPostings(ctx context.Context, collection *mongo.Collection, bookID models.BookID) error {
	_, err := collection.UpdateMany(ctx,
		bson.D{{Key: "postings.book_id", Value: bookID}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "postings", Value: bson.D{{Key: "book_id", Value: bookID}}}}}})
	return err
}

// upsertPostings stores one book's posting inside every term document it touches, in unordered
// batches so a term-dense book stays under the server's per-message limit.
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

// postingUpsert replaces one book's entry inside a term's posting list. The pipeline drops any
// posting already recorded for that book, appends the fresh {book_id, tf} pair and re-sorts by book
// ID, so re-indexing the book converges to the same document instead of stacking duplicates.
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

// postingsWithout projects the stored postings minus every entry for bookID, so the following
// append records exactly one up-to-date entry for that book. It tolerates a missing postings field
// on a brand-new term document.
func postingsWithout(bookID models.BookID) bson.D {
	stored := bson.D{{Key: "$ifNull", Value: bson.A{"$postings", bson.A{}}}}
	dropBook := bson.D{{Key: "$ne", Value: bson.A{"$$this.book_id", bookID}}}
	return bson.D{{Key: "$filter", Value: bson.D{
		{Key: "input", Value: stored},
		{Key: "cond", Value: dropBook},
	}}}
}

// termFrequencies counts how often each token appears in one body, turning the caller's raw token
// stream into the per-term counts the inverted index stores.
func termFrequencies(tokens []string) map[string]int {
	frequencies := make(map[string]int, len(tokens))
	for _, token := range tokens {
		frequencies[token]++
	}
	return frequencies
}
