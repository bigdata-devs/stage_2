package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProbe struct {
	err         error
	calls       int
	deadline    time.Time
	hasDeadline bool
}

func (probe *fakeProbe) Ready(ctx context.Context) error {
	probe.calls++
	probe.deadline, probe.hasDeadline = ctx.Deadline()
	return probe.err
}

func newTestReadinessCheck(t *testing.T, ingestion, indexing *fakeProbe, timeout time.Duration) ReadinessCheck {
	t.Helper()
	check, err := NewReadinessCheck(ingestion, indexing, timeout)
	if err != nil {
		t.Fatalf("NewReadinessCheck: %v", err)
	}
	return check
}

func TestReadinessCheckPassesWhenBothServicesAreReady(t *testing.T) {
	ingestion, indexing := &fakeProbe{}, &fakeProbe{}
	const timeout = 10 * time.Second

	if err := newTestReadinessCheck(t, ingestion, indexing, timeout).Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	for name, probe := range map[string]*fakeProbe{"ingestion": ingestion, "indexing": indexing} {
		if probe.calls != 1 {
			t.Errorf("%s probed %d times, want 1", name, probe.calls)
		}
		if !probe.hasDeadline || time.Until(probe.deadline) > timeout {
			t.Errorf("%s probed without the %s readiness deadline", name, timeout)
		}
	}
}

func TestReadinessCheckReportsAnUnreadyIngestionServiceFirst(t *testing.T) {
	failure := errors.New("connection refused")
	ingestion, indexing := &fakeProbe{err: failure}, &fakeProbe{}

	err := newTestReadinessCheck(t, ingestion, indexing, time.Second).Check(context.Background())

	if !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), "ingestion service is not ready: ") {
		t.Fatalf("Check error = %v, want the ingestion failure", err)
	}
	if indexing.calls != 0 {
		t.Errorf("indexing probed %d times after the ingestion failure, want 0", indexing.calls)
	}
}

func TestReadinessCheckReportsAnUnreadyIndexingService(t *testing.T) {
	failure := errors.New("HTTP 503")

	err := newTestReadinessCheck(t, &fakeProbe{}, &fakeProbe{err: failure}, time.Second).Check(context.Background())

	if !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), "indexing service is not ready: ") {
		t.Fatalf("Check error = %v, want the indexing failure", err)
	}
}

func TestNewReadinessCheckRejectsAnInvalidConfiguration(t *testing.T) {
	if _, err := NewReadinessCheck(nil, &fakeProbe{}, time.Second); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("without ingestion probe: err = %v, want ErrMissingDependency", err)
	}
	if _, err := NewReadinessCheck(&fakeProbe{}, nil, time.Second); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("without indexing probe: err = %v, want ErrMissingDependency", err)
	}
	if _, err := NewReadinessCheck(&fakeProbe{}, &fakeProbe{}, 0); err == nil {
		t.Error("a zero readiness timeout was accepted")
	}
}
