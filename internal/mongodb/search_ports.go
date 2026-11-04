package mongodb

import "boogle-engine/internal/search"

var (
	_ search.Index        = SearchIndex{}
	_ search.CatalogStore = DatamartReader{}
)
