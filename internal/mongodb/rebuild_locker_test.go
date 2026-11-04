package mongodb

import (
	"context"
	"errors"
	"testing"

	"boogle-engine/internal/indexing"
)

func TestRebuildLockerReportsALeaseHeldElsewhereAsRebuildInProgress(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	held, err := client.AcquireRebuildLock(context.Background())
	if err != nil {
		t.Fatalf("acquire the lock of another replica: %v", err)
	}
	locker := NewRebuildLocker(client)

	lease, err := locker.AcquireRebuildLease(context.Background())

	if !errors.Is(err, indexing.ErrRebuildInProgress) || !errors.Is(err, ErrRebuildLocked) {
		t.Fatalf("AcquireRebuildLease while held = %v, want ErrRebuildInProgress wrapping ErrRebuildLocked", err)
	}
	if lease != nil {
		t.Fatalf("AcquireRebuildLease while held returned lease %v, want nil", lease)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("release the lock of another replica: %v", err)
	}
	lease, err = locker.AcquireRebuildLease(context.Background())
	if err != nil {
		t.Fatalf("AcquireRebuildLease after release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}
