package mongodb

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"boogle-engine/internal/models"
)

// DatamartReader reads what the BookIndexer writes: the posting lists of the inverted index and
// the metadata of the books collection. Reads observe committed documents only, so a book always
// appears whole and never blocks a concurrent writer.
type DatamartReader struct {
	client Client
}

// NewDatamartReader wires the reader to a connected datamart.
func NewDatamartReader(client Client) DatamartReader {
	return DatamartReader{client: client}
}

// PostingsForTerms returns the posting list of every requested term the index holds, ordered by
// term so the same input always yields the same output. A term without a posting list is absent
// from the result, and terms must be normalized exactly as the indexer stores them (lowercase,
// stop words removed), because the reader never rewrites them.
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
		if document.Postings == nil {
			document.Postings = []models.Posting{}
		}
		postings = append(postings, models.TermPostings{Term: document.Term, Postings: document.Postings})
	}
	return postings, nil
}

// BooksByID returns the metadata of the requested books, ordered by book ID. Books the datamart
// does not hold are simply absent, so callers that care about a specific book compare the IDs.
func (reader DatamartReader) BooksByID(ctx context.Context, bookIDs []models.BookID) ([]models.BookMetadata, error) {
	if len(bookIDs) == 0 {
		return []models.BookMetadata{}, nil
	}

	var books []models.BookMetadata
	cursor, err := reader.books().Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: bookIDs}}}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find %d books: %w", len(bookIDs), err)
	}
	if err := cursor.All(ctx, &books); err != nil {
		return nil, fmt.Errorf("decode %d books: %w", len(bookIDs), err)
	}
	if books == nil {
		books = []models.BookMetadata{}
	}
	return books, nil
}

// termDocument is the wire shape of one inverted-index record: the normalized term as the _id and
// its posting list.
type termDocument struct {
	Term     string           `bson:"_id"`
	Postings []models.Posting `bson:"postings"`
}

func (reader DatamartReader) books() *mongo.Collection {
	return reader.client.database.Collection(BooksCollection)
}

func (reader DatamartReader) index() *mongo.Collection {
	return reader.client.database.Collection(InvertedIndexCollection)
}
