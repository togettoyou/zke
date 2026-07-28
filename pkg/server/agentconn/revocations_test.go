package agentconn

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// watchAttempt is one scripted outcome of a revocation watch.
type watchAttempt struct {
	// ready reports whether the LISTEN is established before the attempt ends.
	ready bool
	err   error
}

// scriptedConnectionStore plays a fixed sequence of revocation watch outcomes
// so the supervisor's retry policy can be exercised without PostgreSQL.
type scriptedConnectionStore struct {
	mutex    sync.Mutex
	attempts []watchAttempt
	calls    int
}

func (scripted *scriptedConnectionStore) Activate(
	context.Context,
	store.ActivateAgentConnectionParams,
) error {
	return nil
}

func (scripted *scriptedConnectionStore) RecordHeartbeat(
	context.Context,
	store.RecordAgentHeartbeatParams,
) error {
	return nil
}

func (scripted *scriptedConnectionStore) WatchRevocations(
	ctx context.Context,
	onReady func(),
	_ func(store.AgentConnectionRevocation),
) error {
	scripted.mutex.Lock()
	attempt := scripted.attemptAt(scripted.calls)
	scripted.calls++
	scripted.mutex.Unlock()

	if attempt.ready && onReady != nil {
		onReady()
	}
	if attempt.err != nil {
		return attempt.err
	}
	// Past the script the watch is healthy: it runs until cancelled, exactly
	// as the real one does.
	<-ctx.Done()
	return nil
}

func (scripted *scriptedConnectionStore) attemptAt(index int) watchAttempt {
	if index < len(scripted.attempts) {
		return scripted.attempts[index]
	}
	return watchAttempt{ready: true}
}

func (scripted *scriptedConnectionStore) callCount() int {
	scripted.mutex.Lock()
	defer scripted.mutex.Unlock()
	return scripted.calls
}

func supervisorTestManager(connectionStore ConnectionStore) *Manager {
	return &Manager{
		config: Config{LastSeenWriteInterval: time.Minute},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:  connectionStore,
	}
}

// A Server that cannot establish the watch at startup must not start: it would
// be accepting Agents it has no way to disconnect.
func TestSuperviseRevocationsFailsWhenTheFirstWatchNeverStarts(t *testing.T) {
	t.Parallel()

	failure := errors.New("listen refused")
	scripted := &scriptedConnectionStore{
		attempts: []watchAttempt{{ready: false, err: failure}},
	}
	manager := supervisorTestManager(scripted)

	err := manager.superviseRevocations(context.Background(), make(chan struct{}))
	if !errors.Is(err, failure) {
		t.Fatalf("supervise error = %v, want %v", err, failure)
	}
	if calls := scripted.callCount(); calls != 1 {
		t.Fatalf("watch attempts = %d, want 1 without a retry", calls)
	}
}

// Once the watch has run, losing the PostgreSQL connection is an ordinary
// event. Ending the listener there would take the whole Server down, so it
// must be retried instead.
func TestSuperviseRevocationsRetriesAfterAnEstablishedWatchDrops(t *testing.T) {
	t.Parallel()

	scripted := &scriptedConnectionStore{
		attempts: []watchAttempt{
			{ready: true, err: errors.New("connection reset")},
			{ready: true, err: errors.New("connection reset again")},
		},
	}
	manager := supervisorTestManager(scripted)
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	supervised := make(chan error, 1)
	go func() { supervised <- manager.superviseRevocations(ctx, ready) }()

	select {
	case <-ready:
	case err := <-supervised:
		t.Fatalf("supervisor returned before signalling readiness: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor never signalled readiness")
	}

	// The third attempt blocks until cancellation, so reaching it proves both
	// failures were retried rather than returned.
	waitForWatchAttempts(t, scripted, 3)
	cancel()
	select {
	case err := <-supervised:
		if err != nil {
			t.Fatalf("supervise error after cancellation = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
}

// Readiness is reported once. A retry must not close the channel a second
// time, which would panic and take the listener down with it.
func TestSuperviseRevocationsSignalsReadinessOnlyOnce(t *testing.T) {
	t.Parallel()

	scripted := &scriptedConnectionStore{
		attempts: []watchAttempt{
			{ready: true, err: errors.New("first drop")},
			{ready: true, err: errors.New("second drop")},
		},
	}
	manager := supervisorTestManager(scripted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	supervised := make(chan error, 1)
	go func() { supervised <- manager.superviseRevocations(ctx, make(chan struct{})) }()

	waitForWatchAttempts(t, scripted, 3)
	cancel()
	select {
	case err := <-supervised:
		if err != nil {
			t.Fatalf("supervise error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
}

func waitForWatchAttempts(
	t *testing.T,
	scripted *scriptedConnectionStore,
	want int,
) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if scripted.callCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watch attempts = %d, want at least %d", scripted.callCount(), want)
}
