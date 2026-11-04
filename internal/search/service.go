package search

import (
	"context"
	"errors"
	"fmt"

	"boogle-engine/internal/models"
)

var ErrMissingIndex = errors.New("search service needs a search index")

var _ Searcher = (*Service)(nil)

type Service struct {
	index Index
}

func NewService(index Index) (*Service, error) {
	if index == nil {
		return nil, ErrMissingIndex
	}
	return &Service{index: index}, nil
}

func (service *Service) Search(ctx context.Context, request Request) (Result, error) {
	query, err := newQuery(request)
	if err != nil {
		return Result{}, err
	}
	if len(query.Terms) == 0 {
		return Result{Query: query, Page: Page{Books: []models.BookMetadata{}}}, nil
	}
	page, err := service.index.Search(ctx, query)
	if err != nil {
		return Result{}, fmt.Errorf("search index: %w", err)
	}
	if page.Books == nil {
		page.Books = []models.BookMetadata{}
	}
	return Result{Query: query, Page: page}, nil
}
