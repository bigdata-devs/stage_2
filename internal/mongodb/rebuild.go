package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"boogle-engine/internal/indexing"
)

const adminDatabase = "admin"

const namespaceExistsCode = 48

func (indexer BookIndexer) Rebuild(ctx context.Context, next indexing.NextBook) (int, error) {
	temporaryBooks := indexer.client.database.Collection(BooksCollection + rebuildSuffix)
	temporaryIndex := indexer.client.database.Collection(InvertedIndexCollection + rebuildSuffix)
	if err := dropIfExists(ctx, temporaryBooks, temporaryIndex); err != nil {
		return 0, err
	}
	if err := ensureTemporary(ctx, temporaryBooks, temporaryIndex); err != nil {
		return 0, err
	}

	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		book, found, err := next(ctx)
		if err != nil {
			return 0, fmt.Errorf("read next book for rebuild: %w", err)
		}
		if !found {
			break
		}
		if err := writeMetadata(ctx, temporaryBooks, book.Metadata, StatusPublished); err != nil {
			return 0, fmt.Errorf("rebuild metadata for book %d: %w", book.Metadata.BookID, err)
		}
		if err := upsertPostings(ctx, temporaryIndex, book.Metadata.BookID, termFrequencies(book.Tokens)); err != nil {
			return 0, fmt.Errorf("rebuild postings for book %d: %w", book.Metadata.BookID, err)
		}
		processed++
	}

	if _, err := createBookIndexes(ctx, temporaryBooks); err != nil {
		return 0, fmt.Errorf("index rebuilt books: %w", err)
	}
	if _, err := createPostingIndexes(ctx, temporaryIndex); err != nil {
		return 0, fmt.Errorf("index rebuilt postings: %w", err)
	}
	if err := indexer.publishRebuild(ctx, temporaryBooks, temporaryIndex); err != nil {
		return 0, err
	}
	return processed, nil
}

func (indexer BookIndexer) publishRebuild(ctx context.Context, temporaryBooks, temporaryIndex *mongo.Collection) error {
	if err := renameIntoPlace(ctx, temporaryIndex, InvertedIndexCollection); err != nil {
		return err
	}
	if err := renameIntoPlace(ctx, temporaryBooks, BooksCollection); err != nil {
		return err
	}
	return nil
}

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

func ensureTemporary(ctx context.Context, collections ...*mongo.Collection) error {
	for _, collection := range collections {
		command := bson.D{{Key: "create", Value: collection.Name()}}
		err := collection.Database().RunCommand(ctx, command).Err()
		if err == nil {
			continue
		}
		var serverError mongo.ServerError
		if errors.As(err, &serverError) && serverError.HasErrorCode(namespaceExistsCode) {
			continue
		}
		return fmt.Errorf("create %s: %w", collection.Name(), err)
	}
	return nil
}

func dropIfExists(ctx context.Context, collections ...*mongo.Collection) error {
	for _, collection := range collections {
		if err := collection.Drop(ctx); err != nil {
			return fmt.Errorf("drop %s: %w", collection.Name(), err)
		}
	}
	return nil
}
