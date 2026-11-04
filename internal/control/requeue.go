package control

import (
	"fmt"
	"log/slog"

	"boogle-engine/internal/models"
)

type FailureRequeuer struct {
	failures FailureResetter
}

func NewFailureRequeuer(failures FailureResetter) (FailureRequeuer, error) {
	if failures == nil {
		return FailureRequeuer{}, ErrMissingDependency
	}
	return FailureRequeuer{failures: failures}, nil
}

func (requeuer FailureRequeuer) Requeue(candidates []models.BookID) ([]models.BookID, error) {
	retried, err := requeuer.failures.WipeFailures()
	if err != nil {
		return nil, fmt.Errorf("wipe failures: %w", err)
	}
	if len(retried) > 0 {
		slog.Info("[CONTROL] retry-failed: re-enqueuing previously failed books", "count", len(retried))
	}
	requeued := make([]models.BookID, 0, len(candidates)+len(retried))
	requeued = append(requeued, candidates...)
	return append(requeued, retried...), nil
}
