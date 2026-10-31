package main

import (
	"context"

	"boogle-engine/internal/api"
	"boogle-engine/internal/indexer"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

// bookSearcher is the part of mongodb.DatamartReader the engine needs. Depending on this narrow
// interface instead of the concrete reader lets the unit tests check keyword normalization and
// field mapping without a live MongoDB server; production always passes a DatamartReader.
type bookSearcher interface {
	SearchBooks(ctx context.Context, request mongodb.SearchRequest) (mongodb.SearchResult, error)
}

// Compile-time guarantees that the production reader fits the engine and the engine fits the
// handler, so a signature drift in either package breaks the build instead of the service.
var (
	_ bookSearcher     = mongodb.DatamartReader{}
	_ api.SearchEngine = DatamartEngine{}
)

// DatamartEngine answers the Search API from the MongoDB datamart. It normalizes the keyword
// with the same tokenizer the Indexing Service applies to book bodies and delegates ranking,
// filters, pagination and the total count to one server-side aggregation. It is safe for
// concurrent use: every request runs against a stateless reader.
type DatamartEngine struct {
	reader bookSearcher
}

// NewDatamartEngine wires the search API to a connected datamart; production passes
// mongodb.NewDatamartReader(client).
func NewDatamartEngine(reader bookSearcher) DatamartEngine {
	return DatamartEngine{reader: reader}
}

// Search resolves query.Term to the stored form of the inverted index: tokenizer rules decide
// it, so "Adventure!" searches "adventure". GET /search promises one keyword, so of several
// tokens only the first is searched, and a keyword made only of stop words matches nothing
// because those terms are never indexed. The handler has already trimmed, lower-cased and
// bounded every field, so they pass straight through to the datamart request.
func (engine DatamartEngine) Search(ctx context.Context, query api.SearchQuery) (api.SearchPage, error) {
	tokens := indexer.ProcessText(query.Term)
	if len(tokens) == 0 {
		return api.SearchPage{Books: []models.BookMetadata{}}, nil
	}
	result, err := engine.reader.SearchBooks(ctx, mongodb.SearchRequest{
		Term:     tokens[0],
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
