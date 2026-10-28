package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestAcquireRebuildLockExcludesAnotherReplica(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)

	first, err := client.AcquireRebuildLock(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(func() { assertLockReleased(t, first) })

	if _, err := client.AcquireRebuildLock(context.Background()); !errors.Is(err, ErrRebuildLocked) {
		t.Fatalf("second acquire = %v, want ErrRebuildLocked", err)
	}
}

func TestReleaseLetsAnotherReplicaRebuild(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)

	first, err := client.AcquireRebuildLock(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := client.AcquireRebuildLock(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	assertLockReleased(t, second)
}

func TestExpiredRebuildLockIsTakenOver(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	ctx := context.Background()
	_, err := client.Database().Collection(RebuildLocksCollection).InsertOne(ctx, bson.D{
		{Key: "_id", Value: rebuildLockID},
		{Key: "owner", Value: bson.NewObjectID()},
		{Key: "expires_at", Value: time.Now().UTC().Add(-time.Minute)},
	})
	if err != nil {
		t.Fatalf("seed expired lock: %v", err)
	}

	lock, err := client.AcquireRebuildLock(ctx)
	if err != nil {
		t.Fatalf("acquire over an expired lock: %v", err)
	}
	assertLockReleased(t, lock)
}

func TestReleaseKeepsALockOwnedByAnotherReplica(t *testing.T) {
	client := liveClient(t)
	resetDatamart(t, client)
	ctx := context.Background()
	lock, err := client.AcquireRebuildLock(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Model a lease stolen after the holder stalled: Release must not delete it.
	if _, err := client.Database().Collection(RebuildLocksCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: rebuildLockID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "owner", Value: bson.NewObjectID()}}}},
	); err != nil {
		t.Fatalf("steal lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	count, err := client.Database().Collection(RebuildLocksCollection).
		CountDocuments(ctx, bson.D{{Key: "_id", Value: rebuildLockID}})
	if err != nil {
		t.Fatalf("count locks: %v", err)
	}
	if count != 1 {
		t.Fatalf("locks after releasing a stolen lease = %d, want 1", count)
	}
}

func assertLockReleased(t *testing.T, lock *RebuildLock) {
	t.Helper()
	if err := lock.Release(); err != nil {
		t.Errorf("release lock: %v", err)
	}
}
