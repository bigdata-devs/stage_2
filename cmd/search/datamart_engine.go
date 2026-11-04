package main

import (
	"context"

	"boogle-engine/internal/api"
	"boogle-engine/internal/models"
	"boogle-engine/internal/mongodb"
)

type bookSearcher interface {
	SearchBooks(ctx context.Context, request mongodb.SearchRequest) (mongodb.SearchResult, error)
}

var (
	_ bookSearcher     = mongodb.DatamartReader{}
	_ api.SearchEngine = DatamartEngine{}
)

type DatamartEngine struct {
	reader bookSearcher
}

func NewDatamartEngine(reader bookSearcher) DatamartEngine {
	return DatamartEngine{reader: reader}
}

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
