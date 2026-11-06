package control

import (
	"errors"
	"slices"
	"testing"

	"boogle-engine/internal/models"
)

type fakeFailures struct {
	failed []models.BookID
	err    error
	calls  int
}

func (failures *fakeFailures) WipeFailures() ([]models.BookID, error) {
	failures.calls++
	if failures.err != nil {
		return nil, failures.err
	}
	cleared := failures.failed
	failures.failed = nil
	return cleared, nil
}

func newTestRequeuer(t *testing.T, failures *fakeFailures) FailureRequeuer {
	t.Helper()
	requeuer, err := NewFailureRequeuer(failures)
	if err != nil {
		t.Fatalf("NewFailureRequeuer: %v", err)
	}
	return requeuer
}

func TestRequeueAppendsTheClearedFailuresToTheCandidates(t *testing.T) {
	backing := make([]models.BookID, 2, 4)
	backing[0], backing[1] = 1342, 84
	failures := &fakeFailures{failed: []models.BookID{5, 7}}

	requeued, err := newTestRequeuer(t, failures).Requeue(backing)

	if err != nil || !slices.Equal(requeued, []models.BookID{1342, 84, 5, 7}) {
		t.Fatalf("Requeue = %v, %v; want [1342 84 5 7]", requeued, err)
	}
	if spare := backing[:4]; spare[2] != 0 || spare[3] != 0 {
		t.Errorf("Requeue wrote into the caller's slice: %v", spare)
	}
	if failures.calls != 1 {
		t.Errorf("failures wiped %d times, want 1", failures.calls)
	}
}

func TestRequeueWithoutFailuresKeepsTheCandidates(t *testing.T) {
	requeued, err := newTestRequeuer(t, &fakeFailures{}).Requeue([]models.BookID{})

	if err != nil || requeued == nil || len(requeued) != 0 {
		t.Fatalf("Requeue = %v, %v; want an empty non-nil list", requeued, err)
	}
}

func TestRequeueReportsAFailedWipe(t *testing.T) {
	failure := errors.New("disk full")

	if _, err := newTestRequeuer(t, &fakeFailures{err: failure}).Requeue([]models.BookID{1}); !errors.Is(err, failure) {
		t.Fatalf("Requeue error = %v, want the wipe failure", err)
	}
}

func TestNewFailureRequeuerRejectsAMissingStore(t *testing.T) {
	if _, err := NewFailureRequeuer(nil); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("NewFailureRequeuer(nil) = %v, want ErrMissingDependency", err)
	}
}
