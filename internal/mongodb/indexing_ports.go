package mongodb

import "boogle-engine/internal/indexing"

var (
	_ indexing.IndexWriter   = BookIndexer{}
	_ indexing.RebuildLocker = RebuildLocker{}
	_ indexing.IndexMetrics  = DatamartReader{}
)
