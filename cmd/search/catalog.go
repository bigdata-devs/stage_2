package main

import (
	"boogle-engine/internal/api"
	"boogle-engine/internal/search"
)

func buildCatalogHandler(store search.CatalogStore) (api.CatalogHandler, error) {
	catalog, err := search.NewCatalogService(store)
	if err != nil {
		return api.CatalogHandler{}, err
	}
	return api.NewCatalogHandler(catalog)
}
