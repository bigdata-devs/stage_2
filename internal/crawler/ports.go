package crawler

import "boogle-engine/internal/ingestion"

var _ ingestion.BookFetcher = GutenbergClient{}
