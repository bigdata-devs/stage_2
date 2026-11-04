package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

func (reader DatamartReader) PublishedBook(ctx context.Context, bookID models.BookID) (models.BookMetadata, bool, error) {
	var document bookDocument
	err := reader.books().FindOne(ctx, bson.D{
		{Key: "_id", Value: bookID},
		{Key: "status", Value: StatusPublished},
	}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.BookMetadata{}, false, nil
	}
	if err != nil {
		return models.BookMetadata{}, false, fmt.Errorf("find published book %d: %w", bookID, err)
	}
	return document.metadata(), true, nil
}

func (reader DatamartReader) DistinctFilterValues(ctx context.Context) (search.StoredFilterValues, error) {
	published := bson.D{{Key: "status", Value: StatusPublished}}

	var rawLanguages []any
	err := reader.books().Distinct(ctx, "language", published).Decode(&rawLanguages)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return search.StoredFilterValues{}, fmt.Errorf("list distinct languages: %w", err)
	}

	var rawYears []any
	err = reader.books().Distinct(ctx, "publication_year", published).Decode(&rawYears)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return search.StoredFilterValues{}, fmt.Errorf("list distinct publication years: %w", err)
	}
	return search.StoredFilterValues{LanguageLists: stringValues(rawLanguages), Years: integerValues(rawYears)}, nil
}

func stringValues(rawValues []any) []string {
	values := make([]string, 0, len(rawValues))
	for _, rawValue := range rawValues {
		if value, ok := rawValue.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

func integerValues(rawValues []any) []int {
	values := make([]int, 0, len(rawValues))
	for _, rawValue := range rawValues {
		if value, ok := asInt(rawValue); ok {
			values = append(values, value)
		}
	}
	return values
}

func asInt(rawValue any) (int, bool) {
	switch value := rawValue.(type) {
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}
