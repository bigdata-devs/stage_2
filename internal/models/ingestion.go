package models

// IngestionStatus is the outcome of trying to bring one book into the datalake.
type IngestionStatus string

const (
	// StatusDownloaded means the book was downloaded and both of its files were written.
	StatusDownloaded IngestionStatus = "downloaded"
	// StatusAvailable means the datalake already held a complete copy, so nothing was downloaded.
	StatusAvailable IngestionStatus = "available"
	// StatusUnavailable means the book can never be ingested (HTTP 404, missing markers...): do not retry it.
	StatusUnavailable IngestionStatus = "unavailable"
	// StatusFailed means a temporary problem (timeout, HTTP 429/5xx...): the book can be retried later.
	StatusFailed IngestionStatus = "failed"
)

// IngestionResult reports what happened to one book. Path is the book folder when the book is
// stored, and Err explains the failure when Status is StatusUnavailable or StatusFailed.
type IngestionResult struct {
	BookID BookID          `json:"book_id"`
	Status IngestionStatus `json:"status"`
	Path   string          `json:"path,omitempty"`
	Err    error           `json:"-"`
}

// IngestionList reports every complete book of the datalake, as GET /ingest/list answers it.
// Books holds no nil slice: an empty datalake serializes as [].
type IngestionList struct {
	Count int      `json:"count"`
	Books []BookID `json:"books"`
}
