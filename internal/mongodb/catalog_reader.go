package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
)

// PublishedBook returns the metadata of one published book. found is false when the datamart
// holds no published record under that ID: an unknown book and a book still awaiting publication
// are both hidden from readers, exactly like GET /search hides them.
func (reader DatamartReader) PublishedBook(ctx context.Context, bookID models.BookID) (models.BookMetadata, bool, error) {
	var book models.BookMetadata
	err := reader.books().FindOne(ctx, bson.D{
		{Key: "_id", Value: bookID},
		{Key: "status", Value: StatusPublished},
	}).Decode(&book)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.BookMetadata{}, false, nil
	}
	if err != nil {
		return models.BookMetadata{}, false, fmt.Errorf("find published book %d: %w", bookID, err)
	}
	return book, true, nil
}

// FilterOptions returns the distinct filter values of the published books: every ISO 639 code of
// their comma-separated language lists and every known original publication year, each sorted
// ascending. Values that a reader could never match through GET /search are left out: the
// "Unknown" placeholder of books without a language and the year 0 of books without a date.
func (reader DatamartReader) FilterOptions(ctx context.Context) (models.FilterValues, error) {
	values := models.FilterValues{Languages: []string{}, Years: []int{}}
	published := bson.D{{Key: "status", Value: StatusPublished}}

	var rawLanguages []any
	err := reader.books().Distinct(ctx, "language", published).Decode(&rawLanguages)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return models.FilterValues{}, fmt.Errorf("list distinct languages: %w", err)
	}
	values.Languages = flattenLanguages(rawLanguages)

	var rawYears []any
	err = reader.books().Distinct(ctx, "publication_year", published).Decode(&rawYears)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return models.FilterValues{}, fmt.Errorf("list distinct publication years: %w", err)
	}
	values.Years = knownYears(rawYears)
	return values, nil
}

// flattenLanguages splits the stored language lists ("en,fr") into their single codes, drops the
// empty and the "Unknown" placeholder values, de-duplicates the codes and sorts them.
func flattenLanguages(rawValues []any) []string {
	seen := map[string]bool{}
	codes := []string{}
	for _, rawValue := range rawValues {
		list, ok := rawValue.(string)
		if !ok {
			continue
		}
		for _, code := range strings.Split(list, ",") {
			code = strings.TrimSpace(code)
			if code == "" || code == indexer.UnknownValue || seen[code] {
				continue
			}
			seen[code] = true
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	return codes
}

// knownYears collects the publication years the datamart stores as integers, drops the unknown
// value 0 and sorts the rest ascending. BSON integers reach the driver as int32 or int64.
func knownYears(rawValues []any) []int {
	years := []int{}
	for _, rawValue := range rawValues {
		year, ok := asInt(rawValue)
		if !ok || year == 0 {
			continue
		}
		years = append(years, year)
	}
	slices.Sort(years)
	return years
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
