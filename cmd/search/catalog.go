package main

import (
	"boogle-engine/internal/api/searchapi"
	"boogle-engine/internal/search"
)

func buildCatalogHandler(store search.CatalogStore) (searchapi.CatalogHandler, error) {
	catalog, err := search.NewCatalogService(store)
	if err != nil {
		return searchapi.CatalogHandler{}, err
	}
	return searchapi.NewCatalogHandler(catalog)
}
