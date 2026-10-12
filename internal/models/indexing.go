package models

// IndexUpdateResult reports the outcome of POST /index/update/{book_id}.
// Index carries the free-form state string of the answer, e.g. "updated".
type IndexUpdateResult struct {
	BookID BookID `json:"book_id"`
	Index  string `json:"index"`
}

// IndexStats summarises the indexing progress reported by GET /index/status.
// LastUpdate is kept as a string so any service timestamp format decodes safely.
type IndexStats struct {
	BooksIndexed int     `json:"books_indexed"`
	LastUpdate   string  `json:"last_update"`
	IndexSizeMB  float64 `json:"index_size_MB"`
}
