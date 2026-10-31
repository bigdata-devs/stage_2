package main

import (
	"context"

	"boogle-engine/internal/api"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// bookSearcher is the part of mongodb.DatamartReader the engine needs. Depending on this narrow
// interface instead of the concrete reader lets the unit tests check the field mapping and the
// HTTP route without a live MongoDB server; production always passes a DatamartReader.
type bookSearcher interface {
	SearchBooks(ctx context.Context, request mongodb.SearchRequest) (mongodb.SearchResult, error)
}

// Compile-time guarantees that the production reader fits the engine and the engine fits the
// handler, so a signature drift in either package breaks the build instead of the service.
var (
	_ bookSearcher     = mongodb.DatamartReader{}
	_ api.SearchEngine = DatamartEngine{}
)

// DatamartEngine answers the Search API from the MongoDB datamart. The handler has already split
// q into normalized terms with the tokenizer the Indexing Service applies to book bodies; the
// engine hands them to one server-side aggregation that intersects their posting lists (AND) and
// computes ranking, filters, pagination and the total count. It is safe for concurrent use: every
// request runs against a stateless reader.
type DatamartEngine struct {
	reader bookSearcher
}

// NewDatamartEngine wires the search API to a connected datamart; production passes
// mongodb.NewDatamartReader(client).
func NewDatamartEngine(reader bookSearcher) DatamartEngine {
	return DatamartEngine{reader: reader}
}

// Search returns the books that contain every term of query.Terms. The handler has already
// normalized, de-duplicated and bounded the terms and trimmed, lower-cased and bounded every other
// field, so they pass straight through to the datamart request. The handler never sends a query
// without terms; should one arrive, it matches nothing rather than reaching the datamart.
func (engine DatamartEngine) Search(ctx context.Context, query api.SearchQuery) (api.SearchPage, error) {
	if len(query.Terms) == 0 {
		return api.SearchPage{Books: []models.BookMetadata{}}, nil
	}
	result, err := engine.reader.SearchBooks(ctx, mongodb.SearchRequest{
		Terms:    query.Terms,
		Author:   query.Filters.Author,
		Language: query.Filters.Language,
		Year:     query.Filters.Year,
		Offset:   query.Offset,
		Limit:    query.Limit,
	})
	if err != nil {
		return api.SearchPage{}, err
	}
	return api.SearchPage{Total: result.Total, Books: result.Books}, nil
}
