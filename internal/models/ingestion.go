package models

type IngestionStatus string

const (
	StatusDownloaded  IngestionStatus = "downloaded"
	StatusAvailable   IngestionStatus = "available"
	StatusUnavailable IngestionStatus = "unavailable"
	StatusFailed      IngestionStatus = "failed"
)

type IngestionResult struct {
	BookID BookID
	Status IngestionStatus
	Path   string
	Err    error
}
