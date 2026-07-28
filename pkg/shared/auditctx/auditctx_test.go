package auditctx

import (
	"context"
	"errors"
	"testing"
	"time"
)

type requestKey struct{}

func TestDetachSurvivesParentCancellation(t *testing.T) {
	t.Parallel()

	parent, cancelParent := context.WithCancel(context.Background())
	detached, cancelDetached := Detach(parent, time.Minute)
	defer cancelDetached()

	cancelParent()
	<-parent.Done()

	select {
	case <-detached.Done():
		t.Fatal("Detach() context ended with its parent")
	default:
	}
	if err := detached.Err(); err != nil {
		t.Fatalf("Detach() context error = %v, want nil", err)
	}
}

// Dropping cancellation must not drop the bound: an audit write that cannot
// reach the database has to give up on its own.
func TestDetachStillExpires(t *testing.T) {
	t.Parallel()

	detached, cancel := Detach(context.Background(), time.Millisecond)
	defer cancel()

	select {
	case <-detached.Done():
	case <-time.After(time.Second):
		t.Fatal("Detach() context did not expire")
	}
	if !errors.Is(detached.Err(), context.DeadlineExceeded) {
		t.Fatalf("Detach() context error = %v, want DeadlineExceeded", detached.Err())
	}
}

// Request-scoped values have to survive, or detaching would silently strip the
// logging and tracing attributes an audit record is meant to carry.
func TestDetachPreservesValues(t *testing.T) {
	t.Parallel()

	parent := context.WithValue(context.Background(), requestKey{}, "request-1")
	detached, cancel := Detach(parent, time.Minute)
	defer cancel()

	if value, _ := detached.Value(requestKey{}).(string); value != "request-1" {
		t.Fatalf("Detach() dropped request value: %q", value)
	}
}
