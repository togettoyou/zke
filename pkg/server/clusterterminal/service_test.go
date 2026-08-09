package clusterterminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/podexec"
)

type terminalRequesterFake struct {
	requests  []*agentv1.TerminalSessionRequest
	onRequest func(*agentv1.TerminalSessionRequest)
}

func (fake *terminalRequesterFake) RequestTerminalSession(
	_ context.Context,
	_ string,
	request *agentv1.TerminalSessionRequest,
	_ string,
) (*agentv1.TerminalSessionResponse, error) {
	fake.requests = append(fake.requests, request)
	if fake.onRequest != nil {
		fake.onRequest(request)
	}
	return &agentv1.TerminalSessionResponse{
		Result:    agentv1.ResultCode_RESULT_CODE_OK,
		Namespace: request.GetNamespace(),
		PodName:   "zke-terminal-test",
		PodUid:    "pod-uid",
		Container: "terminal",
	}, nil
}

type podExecCreatorFake struct {
	input podexec.CreateInput
}

func (fake *podExecCreatorFake) Create(input podexec.CreateInput) (podexec.Session, error) {
	fake.input = input
	return podexec.Session{
		ID: "exec-session", ClusterID: input.ClusterID, Namespace: input.Namespace,
		PodName: input.PodName, ExpiresAt: input.Now.Add(time.Minute),
	}, nil
}

func TestCreateProjectsOnlyTheSuppliedPermissionSnapshot(t *testing.T) {
	requester := &terminalRequesterFake{}
	podExec := &podExecCreatorFake{}
	service := NewService(requester, podExec, Config{Image: "terminal:test", Namespace: "zke-system", TTL: 10 * time.Minute})
	wantPermissions := []string{"cluster.read", "cluster.terminal.exec"}

	session, err := service.Create(context.Background(), CreateInput{
		UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: wantPermissions, Columns: 120, Rows: 36, Now: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(requester.requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requester.requests))
	}
	request := requester.requests[0]
	if !slices.Equal(request.GetPermissions(), wantPermissions) {
		t.Fatalf("permissions = %v, want %v", request.GetPermissions(), wantPermissions)
	}
	if request.GetNamespace() != "zke-system" || podExec.input.Namespace != "zke-system" {
		t.Fatalf("terminal namespace request=%q exec=%q, want zke-system", request.GetNamespace(), podExec.input.Namespace)
	}
	if slices.Contains(request.GetPermissions(), "cluster.secret.read") || slices.Contains(request.GetPermissions(), "cluster.secret.manage") {
		t.Fatalf("Secret permission was added to snapshot: %v", request.GetPermissions())
	}
	if !podExec.input.Confirm || podExec.input.PodName != "zke-terminal-test" || podExec.input.PodUID != "pod-uid" {
		t.Fatalf("unexpected Pod Exec input: %+v", podExec.input)
	}
	if got := service.Permissions(session.ID); !slices.Equal(got, wantPermissions) {
		t.Fatalf("saved permissions = %v, want %v", got, wantPermissions)
	}

	if err := service.Finish(context.Background(), session.ID); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if len(requester.requests) != 2 || requester.requests[1].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE {
		t.Fatalf("cleanup request = %+v", requester.requests)
	}
	if got := service.Permissions(session.ID); len(got) != 0 {
		t.Fatalf("permissions after Finish = %v, want empty", got)
	}
}

func TestDeterministicTerminalIDIsStableAndScopeBound(t *testing.T) {
	first := deterministicTerminalID("user", "cluster", "namespace", "key")
	if again := deterministicTerminalID("user", "cluster", "namespace", "key"); again != first {
		t.Fatalf("same scope returned %q, want %q", again, first)
	}
	if other := deterministicTerminalID("user", "cluster", "other", "key"); other == first {
		t.Fatalf("different Namespace returned same terminal ID %q", other)
	}
}

func TestTerminalTimeoutsRemainClassifiedForHTTP(t *testing.T) {
	transportErr := terminalRequestError(fmt.Errorf("read Agent response: %w", os.ErrDeadlineExceeded))
	if !errors.Is(transportErr, ErrUpstreamTimeout) {
		t.Fatalf("terminalRequestError() = %v, want ErrUpstreamTimeout", transportErr)
	}
	responseErr := terminalResponseError(&agentv1.TerminalSessionResponse{
		Result: agentv1.ResultCode_RESULT_CODE_TIMEOUT,
		Reason: "Timeout",
	})
	if !errors.Is(responseErr, ErrUpstreamTimeout) {
		t.Fatalf("terminalResponseError() = %v, want ErrUpstreamTimeout", responseErr)
	}
}

func TestCreateCancellationCleansAgentResourcesWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requester := &terminalRequesterFake{}
	requester.onRequest = func(request *agentv1.TerminalSessionRequest) {
		if request.GetAction() == agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE {
			cancel()
		}
	}
	podExec := &podExecCreatorFake{}
	service := NewService(requester, podExec, Config{Image: "terminal:test", TTL: 10 * time.Minute})

	_, err := service.Create(ctx, CreateInput{
		UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: []string{"cluster.terminal.exec"},
		Columns: 120, Rows: 36, Now: time.Unix(100, 0),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context cancellation", err)
	}
	if len(requester.requests) != 2 ||
		requester.requests[1].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE {
		t.Fatalf("requests = %+v, want create followed by detached delete", requester.requests)
	}
	if podExec.input.PodName != "" {
		t.Fatalf("Pod Exec ticket was created after cancellation: %+v", podExec.input)
	}
}

func TestCreateRejectsIdempotencyKeyReuseWithDifferentInputBeforeAgentRequest(t *testing.T) {
	requester := &terminalRequesterFake{}
	service := NewService(requester, &podExecCreatorFake{}, Config{Image: "terminal:test", TTL: 10 * time.Minute})
	input := CreateInput{UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: []string{"cluster.terminal.exec"}, Columns: 120, Rows: 36, Now: time.Unix(100, 0)}
	if _, err := service.Create(context.Background(), input); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	input.Columns = 121
	if _, err := service.Create(context.Background(), input); err != ErrIdempotencyConflict {
		t.Fatalf("conflicting Create() error = %v, want %v", err, ErrIdempotencyConflict)
	}
	if len(requester.requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requester.requests))
	}
}
