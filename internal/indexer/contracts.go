package indexer

import "boogle-engine/internal/models"

type BookSource interface {
	BookReader
	ListBookIDs() ([]models.BookID, error)
}
