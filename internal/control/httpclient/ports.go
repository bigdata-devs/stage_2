package httpclient

import "boogle-engine/internal/control"

var (
	_ control.IngestionService = IngestionClient{}
	_ control.IndexingService  = IndexingClient{}
	_ control.SearchService    = SearchClient{}

	_ control.ReadinessProbe     = IngestionClient{}
	_ control.ReadinessProbe     = IndexingClient{}
	_ control.DatalakeBookLister = IngestionClient{}
)
