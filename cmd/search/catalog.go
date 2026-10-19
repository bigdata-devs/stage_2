package main

import (
	"boogle-engine/internal/api"
	"boogle-engine/internal/mongodb"
)

// buildCatalogHandler performs the dependency injection: the datamart reader supplies the book
// metadata and filter values behind the optional catalog endpoints of the front-end contract.
func buildCatalogHandler(client mongodb.Client) (api.CatalogHandler, error) {
	return api.NewCatalogHandler(mongodb.NewDatamartReader(client))
}
