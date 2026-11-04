package ingestion

import (
	"context"

	"boogle-engine/internal/models"
)

type ingestionCall struct {
	done   chan struct{}
	result models.IngestionResult
}

func (service *Service) join(bookID models.BookID) *ingestionCall {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if call, running := service.inFlight[bookID]; running {
		return call
	}
	call := &ingestionCall{done: make(chan struct{})}
	service.inFlight[bookID] = call
	go service.run(bookID, call)
	return call
}

func (service *Service) run(bookID models.BookID, call *ingestionCall) {
	ctx, cancel := context.WithTimeout(context.Background(), service.timeout)
	defer cancel()
	call.result = service.ingest(ctx, bookID)
	service.forget(bookID)
	close(call.done)
}

func (service *Service) forget(bookID models.BookID) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	delete(service.inFlight, bookID)
}
