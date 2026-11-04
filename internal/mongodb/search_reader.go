package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

var ErrInvalidSearchRequest = errors.New("invalid search request")

type SearchRequest struct {
	Terms    []string
	Author   string
	Language string
	Year     int
	Offset   int
	Limit    int
}

type SearchResult struct {
	Total int
	Books []models.BookMetadata
}

func (reader DatamartReader) SearchBooks(ctx context.Context, request SearchRequest) (SearchResult, error) {
	if err := validateSearchRequest(request); err != nil {
		return SearchResult{}, err
	}

	cursor, err := reader.index().Aggregate(ctx, searchPipeline(request))
	if err != nil {
		return SearchResult{}, fmt.Errorf("search books for %q: %w", strings.Join(request.Terms, " "), err)
	}
	var facets []searchFacet
	if err := cursor.All(ctx, &facets); err != nil {
		return SearchResult{}, fmt.Errorf("decode search results for %q: %w", strings.Join(request.Terms, " "), err)
	}
	return searchResultFrom(facets), nil
}

func validateSearchRequest(request SearchRequest) error {
	if len(request.Terms) == 0 {
		return fmt.Errorf("%w: at least one term is required", ErrInvalidSearchRequest)
	}
	if slices.Contains(request.Terms, "") {
		return fmt.Errorf("%w: terms must not be empty", ErrInvalidSearchRequest)
	}
	if request.Offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", ErrInvalidSearchRequest)
	}
	if request.Limit < 1 {
		return fmt.Errorf("%w: limit must be at least 1", ErrInvalidSearchRequest)
	}
	if request.Year < 0 {
		return fmt.Errorf("%w: year must not be negative", ErrInvalidSearchRequest)
	}
	return nil
}

func searchPipeline(request SearchRequest) bson.A {
	terms := distinctTerms(request.Terms)
	return bson.A{
		bson.D{{Key: "$match", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: terms}}}}}},
		bson.D{{Key: "$unwind", Value: "$postings"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$postings.book_id"},
			{Key: "terms", Value: bson.D{{Key: "$addToSet", Value: "$_id"}}},
			{Key: "score", Value: bson.D{{Key: "$sum", Value: "$postings.tf"}}},
		}}},
		bson.D{{Key: "$match", Value: bson.D{{Key: "$expr", Value: bson.D{{Key: "$eq", Value: bson.A{
			bson.D{{Key: "$size", Value: "$terms"}}, len(terms),
		}}}}}}},
		bson.D{{Key: "$lookup", Value: lookupBook(request)}},
		bson.D{{Key: "$unwind", Value: "$book"}},
		bson.D{{Key: "$facet", Value: bson.D{
			{Key: "total", Value: bson.A{bson.D{{Key: "$count", Value: "count"}}}},
			{Key: "page", Value: pageStages(request)},
		}}},
	}
}

func lookupBook(request SearchRequest) bson.D {
	return bson.D{
		{Key: "from", Value: BooksCollection},
		{Key: "let", Value: bson.D{{Key: "bookId", Value: "$_id"}}},
		{Key: "pipeline", Value: bson.A{bson.D{{Key: "$match", Value: publishedBookMatch(request)}}}},
		{Key: "as", Value: "book"},
	}
}

func publishedBookMatch(request SearchRequest) bson.D {
	match := bson.D{
		{Key: "$expr", Value: bson.D{{Key: "$and", Value: bookConditions(request)}}},
		{Key: "status", Value: StatusPublished},
	}
	if request.Year > 0 {
		match = append(match, bson.E{Key: "publication_year", Value: request.Year})
	}
	return match
}

func bookConditions(request SearchRequest) bson.A {
	conditions := bson.A{bson.D{{Key: "$eq", Value: bson.A{"$_id", "$$bookId"}}}}
	if request.Author != "" {
		conditions = append(conditions, authorCondition(request.Author))
	}
	if request.Language != "" {
		conditions = append(conditions, languageCondition(request.Language))
	}
	return conditions
}

func authorCondition(author string) bson.D {
	position := bson.D{{Key: "$indexOfCP", Value: bson.A{
		bson.D{{Key: "$toLower", Value: "$author"}},
		strings.ToLower(author),
	}}}
	return bson.D{{Key: "$gt", Value: bson.A{position, -1}}}
}

func languageCondition(language string) bson.D {
	storedCodes := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$split", Value: bson.A{
			bson.D{{Key: "$toLower", Value: "$language"}}, ",",
		}}}},
		{Key: "in", Value: bson.D{{Key: "$trim", Value: bson.D{{Key: "input", Value: "$$this"}}}}},
	}}}
	return bson.D{{Key: "$in", Value: bson.A{strings.ToLower(language), storedCodes}}}
}

func pageStages(request SearchRequest) bson.A {
	stages := bson.A{
		bson.D{{Key: "$sort", Value: bson.D{
			{Key: "score", Value: -1},
			{Key: "_id", Value: 1},
		}}},
	}
	if request.Offset > 0 {
		stages = append(stages, bson.D{{Key: "$skip", Value: request.Offset}})
	}
	stages = append(stages,
		bson.D{{Key: "$limit", Value: request.Limit}},
		bson.D{{Key: "$replaceRoot", Value: bson.D{{Key: "newRoot", Value: "$book"}}}},
	)
	return stages
}

func distinctTerms(terms []string) []string {
	distinct := make([]string, 0, len(terms))
	for _, term := range terms {
		if !slices.Contains(distinct, term) {
			distinct = append(distinct, term)
		}
	}
	return distinct
}

func searchResultFrom(facets []searchFacet) SearchResult {
	result := SearchResult{Books: []models.BookMetadata{}}
	if len(facets) == 0 {
		return result
	}
	if len(facets[0].Total) > 0 {
		result.Total = int(facets[0].Total[0].Count)
	}
	if facets[0].Page != nil {
		result.Books = facets[0].Page
	}
	return result
}

type facetCount struct {
	Count int64 `bson:"count"`
}

type searchFacet struct {
	Total []facetCount          `bson:"total"`
	Page  []models.BookMetadata `bson:"page"`
}
