package control

import (
	"context"
	"fmt"
	"time"
)

type ReadinessCheck struct {
	ingestion ReadinessProbe
	indexing  ReadinessProbe
	timeout   time.Duration
}

func NewReadinessCheck(ingestion ReadinessProbe, indexing ReadinessProbe, timeout time.Duration) (ReadinessCheck, error) {
	if ingestion == nil || indexing == nil {
		return ReadinessCheck{}, ErrMissingDependency
	}
	if timeout <= 0 {
		return ReadinessCheck{}, fmt.Errorf("readiness timeout must be positive, got %s", timeout)
	}
	return ReadinessCheck{ingestion: ingestion, indexing: indexing, timeout: timeout}, nil
}

func (check ReadinessCheck) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, check.timeout)
	defer cancel()
	if err := check.ingestion.Ready(ctx); err != nil {
		return fmt.Errorf("ingestion service is not ready: %w", err)
	}
	if err := check.indexing.Ready(ctx); err != nil {
		return fmt.Errorf("indexing service is not ready: %w", err)
	}
	return nil
}
