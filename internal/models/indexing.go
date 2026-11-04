package models

type IndexUpdateResult struct {
	BookID BookID `json:"book_id"`
	Index  string `json:"index"`
}

type IndexStats struct {
	BooksIndexed int     `json:"books_indexed"`
	LastUpdate   string  `json:"last_update"`
	IndexSizeMB  float64 `json:"index_size_MB"`
}
