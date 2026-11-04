package mongodb

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (reader DatamartReader) CountPublishedBooks(ctx context.Context) (int, error) {
	count, err := reader.books().CountDocuments(ctx, bson.D{{Key: "status", Value: StatusPublished}})
	if err != nil {
		return 0, fmt.Errorf("count published books: %w", err)
	}
	return int(count), nil
}

func (reader DatamartReader) DataSizeBytes(ctx context.Context) (uint64, error) {
	var stats struct {
		DataSize int64 `bson:"dataSize"`
	}
	command := bson.D{{Key: "dbStats", Value: 1}}
	if err := reader.client.database.RunCommand(ctx, command).Decode(&stats); err != nil {
		return 0, fmt.Errorf("measure datamart size: %w", err)
	}
	return uint64(max(stats.DataSize, 0)), nil
}
