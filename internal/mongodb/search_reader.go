package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"boogle-engine/internal/models"
)

// ErrInvalidSearchRequest reports a SearchRequest the reader cannot serve: an empty term, a
// negative offset, a limit below one or a negative year. The Search Service validates requests
// before they reach the datamart; the reader re-checks so a bad request can never turn into an
// unbounded query.
var ErrInvalidSearchRequest = errors.New("invalid search request")

// SearchRequest is one keyword search over the datamart. Term must be normalized exactly as the
// indexer stores it (lowercase, stop words removed, see indexer.ProcessText): the reader matches
// it verbatim. Author narrows results to books whose author contains the text ignoring case,
// Language to books whose comma-separated list contains the code, and Year (0 for none) to books
// published that exact year. Offset and Limit select one page; Limit must be at least 1.
type SearchRequest struct {
	Term     string
	Author   string
	Language string
	Year     int
	Offset   int
	Limit    int
}

// SearchResult carries one page of matching books plus the total number of matches across every
// page, so the caller can render pagination. Books is never nil; on no matches it is empty.
type SearchResult struct {
	Total int
	Books []models.BookMetadata
}

// SearchBooks answers one keyword search in a single server round trip: it joins the term's
// posting list with published book metadata, applies the optional author, language and year
// filters, counts every match and returns one page ordered by relevance (descending term
// frequency, ties broken by ascending book ID). Books still pending or already filtered out
// never reach the page and never count towards Total.
func (reader DatamartReader) SearchBooks(ctx context.Context, request SearchRequest) (SearchResult, error) {
	if err := validateSearchRequest(request); err != nil {
		return SearchResult{}, err
	}

	cursor, err := reader.index().Aggregate(ctx, searchPipeline(request))
	if err != nil {
		return SearchResult{}, fmt.Errorf("search books for %q: %w", request.Term, err)
	}
	var facets []searchFacet
	if err := cursor.All(ctx, &facets); err != nil {
		return SearchResult{}, fmt.Errorf("decode search results for %q: %w", request.Term, err)
	}
	return searchResultFrom(facets), nil
}

func validateSearchRequest(request SearchRequest) error {
	if request.Term == "" {
		return fmt.Errorf("%w: term must not be empty", ErrInvalidSearchRequest)
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

// searchPipeline matches the term document, fans its postings out to their book records, drops
// every book that is not published or fails the filters, and packs the survivors into a total
// count and a relevance-ordered page. With no matching term document the facet yields no document
// at all, which searchResultFrom reports as an empty result.
func searchPipeline(request SearchRequest) bson.A {
	return bson.A{
		bson.D{{Key: "$match", Value: bson.D{{Key: "_id", Value: request.Term}}}},
		bson.D{{Key: "$unwind", Value: "$postings"}},
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
		{Key: "let", Value: bson.D{{Key: "bookId", Value: "$postings.book_id"}}},
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
			{Key: "postings.tf", Value: -1},
			{Key: "postings.book_id", Value: 1},
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

// facetCount is the single $count document the total facet emits when at least one book matched.
type facetCount struct {
	Count int64 `bson:"count"`
}

// searchFacet is the wire shape of the aggregation's one result document: the total count
// (absent without matches) and the relevance-ordered page of book metadata, whose extra status
// field the decoder discards.
type searchFacet struct {
	Total []facetCount          `bson:"total"`
	Page  []models.BookMetadata `bson:"page"`
}
