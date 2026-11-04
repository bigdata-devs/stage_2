package main

import (
	"boogle-engine/internal/api"
	"boogle-engine/internal/mongodb"
)

func buildCatalogHandler(reader mongodb.DatamartReader) (api.CatalogHandler, error) {
	return api.NewCatalogHandler(reader)
}
