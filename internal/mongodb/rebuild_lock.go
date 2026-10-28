package mongodb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// RebuildLocksCollection holds the single lease document that serializes rebuilds across every
// replica that shares the datamart.
const RebuildLocksCollection = "locks"

// rebuildLockID is the _id of that lease document.
const rebuildLockID = "rebuild"

const (
	// rebuildLockTTL keeps a lease valid without a renewal. A replica that crashes mid-rebuild
	// therefore blocks the others for at most this long.
	rebuildLockTTL = 2 * time.Minute
	// rebuildLockRenewInterval renews well inside the TTL, so two missed renewals still leave the
	// holder's lease valid.
	rebuildLockRenewInterval = 30 * time.Second
	// rebuildLockRoundTripTimeout bounds a renewal or a release. The release must run even when
	// the caller's context has already expired, so both use their own background context.
	rebuildLockRoundTripTimeout = 5 * time.Second
)

// ErrRebuildLocked is returned by AcquireRebuildLock while another process holds the lease.
var ErrRebuildLocked = errors.New("another process is rebuilding the datamart")

// RebuildLock is the lease that allows one rebuild at a time across every replica sharing the
// datamart. The holder renews it in the background; after Release, or a crash once the lease
// expires, another replica may claim it.
type RebuildLock struct {
	collection *mongo.Collection
	owner      bson.ObjectID
	stop       chan struct{}
	done       chan struct{}
	release    sync.Once
}

// AcquireRebuildLock claims the cluster-wide rebuild lease. It returns ErrRebuildLocked when a
// live lease exists; an expired one (left by a replica that died mid-rebuild) is taken over.
func (client Client) AcquireRebuildLock(ctx context.Context) (*RebuildLock, error) {
	collection := client.database.Collection(RebuildLocksCollection)
	owner := bson.NewObjectID()
	now := time.Now().UTC()
	expired := bson.D{
		{Key: "_id", Value: rebuildLockID},
		{Key: "expires_at", Value: bson.D{{Key: "$lt", Value: now}}},
	}
	claim := bson.D{{Key: "$set", Value: bson.D{
		{Key: "owner", Value: owner},
		{Key: "expires_at", Value: now.Add(rebuildLockTTL)},
	}}}
	takenOver, err := collection.UpdateOne(ctx, expired, claim)
	if err != nil {
		return nil, fmt.Errorf("take over expired rebuild lock: %w", err)
	}
	if takenOver.MatchedCount == 0 {
		document := bson.D{
			{Key: "_id", Value: rebuildLockID},
			{Key: "owner", Value: owner},
			{Key: "expires_at", Value: now.Add(rebuildLockTTL)},
		}
		if _, err := collection.InsertOne(ctx, document); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, ErrRebuildLocked
			}
			return nil, fmt.Errorf("create rebuild lock: %w", err)
		}
	}
	lock := &RebuildLock{
		collection: collection,
		owner:      owner,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go lock.renew()
	return lock, nil
}

// renew extends the lease in the background until Release stops it or the lease is lost.
func (lock *RebuildLock) renew() {
	defer close(lock.done)
	ticker := time.NewTicker(rebuildLockRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-lock.stop:
			return
		case <-ticker.C:
			if !lock.extend() {
				return
			}
		}
	}
}

// extend pushes the expiry forward and reports whether the lease is still held.
func (lock *RebuildLock) extend() bool {
	ctx, cancel := context.WithTimeout(context.Background(), rebuildLockRoundTripTimeout)
	defer cancel()
	result, err := lock.collection.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: rebuildLockID}, {Key: "owner", Value: lock.owner}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "expires_at", Value: time.Now().UTC().Add(rebuildLockTTL)}}}})
	if err != nil {
		slog.Warn("[MONGODB] rebuild lock renewal failed", "error", err)
		return true
	}
	if result.MatchedCount == 0 {
		slog.Warn("[MONGODB] rebuild lock lost to another replica")
		return false
	}
	return true
}

// Release stops the renewal and deletes the lease if this holder still owns it. It uses its own
// bounded context, because a rebuild that failed or timed out must still free the lease. Calling
// it more than once is safe.
func (lock *RebuildLock) Release() error {
	var err error
	lock.release.Do(func() {
		close(lock.stop)
		<-lock.done
		ctx, cancel := context.WithTimeout(context.Background(), rebuildLockRoundTripTimeout)
		defer cancel()
		_, err = lock.collection.DeleteOne(ctx,
			bson.D{{Key: "_id", Value: rebuildLockID}, {Key: "owner", Value: lock.owner}})
	})
	return err
}
