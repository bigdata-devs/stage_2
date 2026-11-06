package datalake

import (
	"boogle-engine/internal/indexing"
	"boogle-engine/internal/ingestion"
)

var (
	_ indexing.BookSource = Datalake{}
	_ ingestion.BookStore = Datalake{}
)
