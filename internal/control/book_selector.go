package control

import (
	"sync"

	"boogle-engine/internal/models"
)

type BookSelector struct {
	mutex      sync.Mutex
	candidates []models.BookID
	position   int
}

func NewBookSelector(candidates []models.BookID) BookSelector {
	seen := make(map[models.BookID]struct{}, len(candidates))
	unique := make([]models.BookID, 0, len(candidates))
	for _, bookID := range candidates {
		if _, repeated := seen[bookID]; repeated {
			continue
		}
		seen[bookID] = struct{}{}
		unique = append(unique, bookID)
	}
	return BookSelector{candidates: unique}
}

func (selector *BookSelector) Claim() (models.BookID, bool) {
	selector.mutex.Lock()
	defer selector.mutex.Unlock()
	if selector.position >= len(selector.candidates) {
		return 0, false
	}
	bookID := selector.candidates[selector.position]
	selector.position++
	return bookID, true
}
