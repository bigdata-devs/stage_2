package mongodb

import (
	"context"

	"boogle-engine/internal/search"
)

type booksSearcher interface {
	SearchBooks(ctx context.Context, request SearchRequest) (SearchResult, error)
}

type SearchIndex struct {
	searcher booksSearcher
}

func NewSearchIndex(reader DatamartReader) SearchIndex {
	return SearchIndex{searcher: reader}
}

func (index SearchIndex) Search(ctx context.Context, query search.Query) (search.Page, error) {
	result, err := index.searcher.SearchBooks(ctx, SearchRequest{
		Terms:    query.Terms,
		Author:   query.Filters.Author,
		Language: query.Filters.Language,
		Year:     query.Filters.Year,
		Offset:   query.Offset,
		Limit:    query.Limit,
	})
	if err != nil {
		return search.Page{}, err
	}
	return search.Page{Total: result.Total, Books: result.Books}, nil
}
