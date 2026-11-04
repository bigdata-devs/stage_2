package models

type IngestionStatus string

const (
	StatusDownloaded  IngestionStatus = "downloaded"
	StatusAvailable   IngestionStatus = "available"
	StatusUnavailable IngestionStatus = "unavailable"
	StatusFailed      IngestionStatus = "failed"
)

type IngestionResult struct {
	BookID BookID          `json:"book_id"`
	Status IngestionStatus `json:"status"`
	Path   string          `json:"path,omitempty"`
	Err    error           `json:"-"`
}

type IngestionList struct {
	Count int      `json:"count"`
	Books []BookID `json:"books"`
}
