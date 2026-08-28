package helm

import (
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

func installSpec(actor string) OperationSpec {
	return OperationSpec{
		ClusterID:   "00000000-0000-4000-8000-000000000009",
		Namespace:   "shop",
		ReleaseName: "checkout",
		Action:      OperationInstall,
		Chart:       "demo",
		ActorUserID: actor,
	}
}

// The account is written as the operation happens and closed with what it
// produced. Reading it mid-flight is the whole point, so what a reader gets is
// a copy: the log is still growing behind them.
func TestOperationRecordsProgressAndOutcome(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	started, existing, err := operations.Start(installSpec("alice"))
	if err != nil || existing {
		t.Fatalf("Start() = %v, existing=%v", err, existing)
	}
	if started.Status != OperationRunning || started.Events == nil {
		t.Fatalf("started = %+v", started)
	}

	operations.Append(started.ID, StageResolvingChart, "resolving chart demo@latest")
	snapshot, _ := operations.Get(started.ID, "alice")
	operations.Append(started.ID, StageExecuting, "creating 6 resource(s)")
	if len(snapshot.Events) != 1 {
		t.Fatalf("a snapshot grew after it was handed out: %+v", snapshot.Events)
	}

	operations.Finish(started.ID, &helmrelease.Report{
		Name:     "checkout",
		Revision: 3,
		Manifest: "kind: Deployment\n",
	}, nil)
	finished, found := operations.Get(started.ID, "alice")
	if !found || finished.Status != OperationSucceeded {
		t.Fatalf("finished = %+v found=%v", finished, found)
	}
	if finished.Stage != StageExecuting || finished.FinishedAt == nil {
		t.Fatalf("finished = %+v", finished)
	}
	if finished.Report == nil || finished.Report.Revision != 3 {
		t.Fatalf("report = %+v", finished.Report)
	}
	last := finished.Events[len(finished.Events)-1].Message
	if !strings.Contains(last, "revision 3") {
		t.Fatalf("closing line = %q", last)
	}

	// A line arriving after the answer is dropped rather than reopening it.
	operations.Append(started.ID, StageExecuting, "too late")
	reread, _ := operations.Get(started.ID, "alice")
	if len(reread.Events) != len(finished.Events) {
		t.Fatalf("the account grew after it was closed: %+v", reread.Events)
	}
}

// A failure is recorded in the same code the synchronous API would have
// returned, because it is read by the same client code.
func TestOperationRecordsFailureWithItsCode(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	started, _, _ := operations.Start(installSpec("alice"))
	operations.Append(started.ID, StageExecuting, "")
	operations.Finish(started.ID, nil, &OperationFailure{
		Code:    "helm_chart_cross_namespace",
		Message: `chart renders ConfigMap/stolen into Namespace "kube-system"`,
	})
	failed, _ := operations.Get(started.ID, "alice")
	if failed.Status != OperationFailed || failed.Failure == nil ||
		failed.Failure.Code != "helm_chart_cross_namespace" {
		t.Fatalf("failed = %+v", failed)
	}
	if failed.Stage != StageExecuting {
		t.Fatalf("a failure lost the stage it failed in: %+v", failed)
	}
	last := failed.Events[len(failed.Events)-1].Message
	if !strings.Contains(last, "kube-system") {
		t.Fatalf("closing line = %q", last)
	}
}

// One key, one operation. This is what makes a Console that retried a lost 202
// a retry rather than a second install — and what keeps the same key used for a
// different request from being answered with the wrong account.
func TestOperationIdempotencyKeyClaimsOneRequest(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	spec := installSpec("alice")
	spec.IdempotencyKey = "console-submission-0001"
	first, existing, err := operations.Start(spec)
	if err != nil || existing {
		t.Fatalf("first Start() = %v existing=%v", err, existing)
	}
	repeat, existing, err := operations.Start(spec)
	if err != nil || !existing || repeat.ID != first.ID {
		t.Fatalf("retry = %+v existing=%v err=%v", repeat, existing, err)
	}

	other := spec
	other.ReleaseName = "payments"
	if _, _, err := operations.Start(other); err != ErrOperationIdempotencyConflict {
		t.Fatalf("a key reused for another request gave %v", err)
	}

	// A key held by one operator says nothing about another's request.
	elsewhere := spec
	elsewhere.ActorUserID = "bob"
	if _, _, err := operations.Start(elsewhere); err != ErrOperationIdempotencyConflict {
		t.Fatalf("a key reused by another operator gave %v", err)
	}
}

// An operation's report carries a rendered manifest, which can hold a Secret
// the chart generated. It is readable by the operator it was already returned
// to, and by nobody else.
func TestOperationIsReadableOnlyByItsOperator(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	started, _, _ := operations.Start(installSpec("alice"))
	if _, found := operations.Get(started.ID, "bob"); found {
		t.Fatal("another operator read the operation")
	}
	if _, found := operations.Get(started.ID, ""); found {
		t.Fatal("an unidentified caller read the operation")
	}
	if listed := operations.List(started.ClusterID, "shop", "bob"); len(listed) != 0 {
		t.Fatalf("another operator listed %+v", listed)
	}
	listed := operations.List(started.ClusterID, "shop", "alice")
	if len(listed) != 1 || listed[0].ID != started.ID {
		t.Fatalf("listing = %+v", listed)
	}
	// A listing is a way back to an operation, not a way to read one.
	if len(listed[0].Events) != 0 || listed[0].Report != nil {
		t.Fatalf("the listing carried the account: %+v", listed[0])
	}
}

// A log longer than the bound keeps both ends: the beginning says what the
// operation set out to do and the end says how it went. The thousand identical
// wait polls in between are the part nobody reads.
func TestOperationLogKeepsBothEnds(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	started, _, _ := operations.Start(installSpec("alice"))
	operations.Append(started.ID, StageResolvingChart, "first line")
	for index := range maxOperationEvents * 2 {
		operations.Append(started.ID, StageExecuting, "poll "+strings.Repeat("x", index%3))
	}
	operations.Append(started.ID, StageExecuting, "last line")
	account, _ := operations.Get(started.ID, "alice")
	if !account.EventsTruncated {
		t.Fatal("a log past its bound was not reported as truncated")
	}
	if len(account.Events) > maxOperationEvents {
		t.Fatalf("log holds %d lines", len(account.Events))
	}
	if account.Events[0].Message != "first line" {
		t.Fatalf("the beginning was dropped: %q", account.Events[0].Message)
	}
	if account.Events[len(account.Events)-1].Message != "last line" {
		t.Fatalf("the end was dropped: %q", account.Events[len(account.Events)-1].Message)
	}
}

// A finished operation is forgotten once nobody is plausibly still reading it.
// A running one never is: its account is the only place anyone can find out
// what it is doing.
func TestOperationEvictionSparesWhatIsStillRunning(t *testing.T) {
	t.Parallel()

	operations := NewOperations()
	clock := time.Now()
	operations.now = func() time.Time { return clock }

	done, _, _ := operations.Start(installSpec("alice"))
	operations.Finish(done.ID, &helmrelease.Report{Name: "checkout"}, nil)
	running, _, _ := operations.Start(installSpec("alice"))

	clock = clock.Add(operationRetention + time.Minute)
	if _, found := operations.Get(done.ID, "alice"); found {
		t.Fatal("a finished operation outlived its retention")
	}
	if _, found := operations.Get(running.ID, "alice"); !found {
		t.Fatal("a running operation was evicted")
	}

	// The key it claimed goes with it, so a much later retry starts afresh
	// rather than colliding with a record nobody can read.
	spec := installSpec("alice")
	spec.IdempotencyKey = "console-submission-0002"
	expired, _, _ := operations.Start(spec)
	operations.Finish(expired.ID, &helmrelease.Report{Name: "checkout"}, nil)
	clock = clock.Add(operationRetention + time.Minute)
	if _, existing, err := operations.Start(spec); err != nil || existing {
		t.Fatalf("a forgotten key blocked a fresh submission: existing=%v err=%v", existing, err)
	}
}

func TestIsOperationIDAcceptsOnlyWhatIsIssued(t *testing.T) {
	t.Parallel()

	identifier, err := newOperationID()
	if err != nil || !IsOperationID(identifier) {
		t.Fatalf("newOperationID() = %q, %v", identifier, err)
	}
	for _, value := range []string{
		"",
		"short",
		strings.Repeat("g", 32),
		strings.ToUpper(identifier),
		identifier + "0",
	} {
		if IsOperationID(value) {
			t.Errorf("IsOperationID(%q) = true", value)
		}
	}
}
