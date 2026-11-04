package mongodb

import (
	"context"
	"errors"
	"fmt"

	"boogle-engine/internal/indexing"
)

type RebuildLocker struct {
	client Client
}

func NewRebuildLocker(client Client) RebuildLocker {
	return RebuildLocker{client: client}
}

func (locker RebuildLocker) AcquireRebuildLease(ctx context.Context) (indexing.RebuildLease, error) {
	lock, err := locker.client.AcquireRebuildLock(ctx)
	if errors.Is(err, ErrRebuildLocked) {
		return nil, fmt.Errorf("%w: %w", indexing.ErrRebuildInProgress, err)
	}
	if err != nil {
		return nil, err
	}
	return lock, nil
}
