package search

import (
	"context"

	"boogle-engine/internal/models"
)

type Searcher interface {
	Search(ctx context.Context, request Request) (Result, error)
}

type Index interface {
	Search(ctx context.Context, query Query) (Page, error)
}

type Request struct {
	Text     string
	Author   string
	Language string
	Year     *int
	Offset   *int
	Limit    *int
}

type Filters struct {
	Author   string
	Language string
	Year     int
}

type Query struct {
	Text    string
	Terms   []string
	Filters Filters
	Offset  int
	Limit   int
}

type Page struct {
	Total int
	Books []models.BookMetadata
}

type Result struct {
	Query Query
	Page  Page
}
