package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"boogle-engine/internal/models"
)

// adminDatabase owns cluster-wide commands; renameCollection only runs against it.
const adminDatabase = "admin"

// RebuildResult reports what a rebuild produced: how many books it stored and how long it took.
type RebuildResult struct {
	BooksProcessed int
	Elapsed        time.Duration
}

// Rebuild replaces the whole datamart with the given books. It writes them into temporary
// collections, creates the query indexes there, and only then renames both into place, so search
// never observes a half-built index. Each rename is atomic, but the two renames are not a single
// transaction: for the few milliseconds between them a reader can pair new postings with old
// metadata. That window is negligible on the single-machine Stage 2 host; a Stage 3 cluster should
// replace the renames with a version pointer search resolves before querying. Rebuild is meant for
// offline, one-at-a-time use, so it does not guard against two rebuilds racing.
func (indexer BookIndexer) Rebuild(ctx context.Context, books []models.IndexBookInput) (RebuildResult, error) {
	started := time.Now()
	temporaryBooks := indexer.client.database.Collection(BooksCollection + rebuildSuffix)
	temporaryIndex := indexer.client.database.Collection(InvertedIndexCollection + rebuildSuffix)
	if err := dropIfExists(ctx, temporaryBooks, temporaryIndex); err != nil {
		return RebuildResult{}, err
	}

	for _, book := range books {
		if err := writeMetadata(ctx, temporaryBooks, book.Metadata, StatusPublished); err != nil {
			return RebuildResult{}, fmt.Errorf("rebuild metadata for book %d: %w", book.Metadata.BookID, err)
		}
		if err := upsertPostings(ctx, temporaryIndex, book.Metadata.BookID, termFrequencies(book.Tokens)); err != nil {
			return RebuildResult{}, fmt.Errorf("rebuild postings for book %d: %w", book.Metadata.BookID, err)
		}
	}

	if _, err := createBookIndexes(ctx, temporaryBooks); err != nil {
		return RebuildResult{}, fmt.Errorf("index rebuilt books: %w", err)
	}
	if err := indexer.publishRebuild(ctx, temporaryBooks, temporaryIndex); err != nil {
		return RebuildResult{}, err
	}
	return RebuildResult{BooksProcessed: len(books), Elapsed: time.Since(started)}, nil
}

// publishRebuild swaps the temporary collections over the live ones, index first so that a reader
// in the tiny window between the two renames still resolves every new posting to metadata that is
// about to be published.
func (indexer BookIndexer) publishRebuild(ctx context.Context, temporaryBooks, temporaryIndex *mongo.Collection) error {
	if err := renameIntoPlace(ctx, temporaryIndex, InvertedIndexCollection); err != nil {
		return err
	}
	if err := renameIntoPlace(ctx, temporaryBooks, BooksCollection); err != nil {
		return err
	}
	return nil
}

// renameIntoPlace atomically replaces the live collection with a fully built temporary one. The
// command only runs against the admin database, and dropTarget clears whatever held the name.
func renameIntoPlace(ctx context.Context, temporary *mongo.Collection, live string) error {
	database := temporary.Database()
	temporaryName := fmt.Sprintf("%s.%s", database.Name(), temporary.Name())
	command := bson.D{
		{Key: "renameCollection", Value: temporaryName},
		{Key: "to", Value: fmt.Sprintf("%s.%s", database.Name(), live)},
		{Key: "dropTarget", Value: true},
	}
	admin := database.Client().Database(adminDatabase)
	if err := admin.RunCommand(ctx, command).Err(); err != nil {
		return fmt.Errorf("swap %s into %s: %w", temporaryName, live, err)
	}
	return nil
}

// dropIfExists removes temporary collections left behind by a rebuild that failed before it could
// swap, so a retry always starts from a clean slate.
func dropIfExists(ctx context.Context, collections ...*mongo.Collection) error {
	for _, collection := range collections {
		if err := collection.Drop(ctx); err != nil {
			return fmt.Errorf("drop %s: %w", collection.Name(), err)
		}
	}
	return nil
}
