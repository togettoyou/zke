package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type terminalPermissionResolverFake struct {
	allowed map[rbac.Permission]bool
}

type revokingPermissionResolverFake struct {
	revoked atomic.Bool
}

func (fake *revokingPermissionResolverFake) AuthorizeCluster(
	_ context.Context,
	_ string,
	permission rbac.Permission,
	_ string,
) (rbac.ResolvedScope, error) {
	if permission == rbac.PermissionClusterTerminalExec && !fake.revoked.Load() {
		return rbac.ResolvedScope{}, nil
	}
	return rbac.ResolvedScope{}, rbac.ErrDenied
}

type blockingTerminalCommanderFake struct {
	started chan struct{}
	closed  atomic.Int32
}

type idleTerminalCommanderFake struct {
	created atomic.Int32
	closed  atomic.Int32
	done    chan struct{}
}

type failingTerminalCommanderFake struct {
	created atomic.Int32
}

func (fake *failingTerminalCommanderFake) CreateCommandSession(
	_ context.Context,
	_ clusterterminal.CommandSessionInput,
) (clusterterminal.CommandSession, error) {
	fake.created.Add(1)
	return clusterterminal.CommandSession{}, clusterterminal.ErrUpstreamFailure
}

func (*failingTerminalCommanderFake) ExecuteCommand(
	context.Context,
	clusterterminal.CommandInput,
) (clusterterminal.CommandResult, error) {
	return clusterterminal.CommandResult{}, nil
}

func (*failingTerminalCommanderFake) FinishCommandSession(
	context.Context,
	clusterterminal.CommandSession,
) error {
	return nil
}

func (fake *idleTerminalCommanderFake) CreateCommandSession(
	_ context.Context,
	input clusterterminal.CommandSessionInput,
) (clusterterminal.CommandSession, error) {
	fake.created.Add(1)
	return testCommandSession(input), nil
}

func (fake *idleTerminalCommanderFake) ExecuteCommand(
	_ context.Context,
	_ clusterterminal.CommandInput,
) (clusterterminal.CommandResult, error) {
	return clusterterminal.CommandResult{Stdout: "ok\n"}, nil
}

func (fake *idleTerminalCommanderFake) FinishCommandSession(
	_ context.Context,
	_ clusterterminal.CommandSession,
) error {
	if fake.closed.Add(1) == 1 {
		close(fake.done)
	}
	return nil
}

func (fake *blockingTerminalCommanderFake) CreateCommandSession(
	_ context.Context,
	input clusterterminal.CommandSessionInput,
) (clusterterminal.CommandSession, error) {
	return testCommandSession(input), nil
}

func (fake *blockingTerminalCommanderFake) ExecuteCommand(
	ctx context.Context,
	_ clusterterminal.CommandInput,
) (clusterterminal.CommandResult, error) {
	close(fake.started)
	<-ctx.Done()
	return clusterterminal.CommandResult{Stdout: "partial\n"}, ctx.Err()
}

func (fake *blockingTerminalCommanderFake) FinishCommandSession(
	_ context.Context,
	_ clusterterminal.CommandSession,
) error {
	fake.closed.Add(1)
	return nil
}

func (fake terminalPermissionResolverFake) AuthorizeCluster(
	_ context.Context,
	_ string,
	permission rbac.Permission,
	_ string,
) (rbac.ResolvedScope, error) {
	if fake.allowed[permission] {
		return rbac.ResolvedScope{}, nil
	}
	return rbac.ResolvedScope{}, rbac.ErrDenied
}

type terminalCommanderFake struct {
	created  []clusterterminal.CommandSessionInput
	executed []clusterterminal.CommandInput
	closed   []clusterterminal.CommandSession
}

func (fake *terminalCommanderFake) CreateCommandSession(
	_ context.Context,
	input clusterterminal.CommandSessionInput,
) (clusterterminal.CommandSession, error) {
	fake.created = append(fake.created, input)
	return testCommandSession(input), nil
}

func (fake *terminalCommanderFake) ExecuteCommand(
	_ context.Context,
	input clusterterminal.CommandInput,
) (clusterterminal.CommandResult, error) {
	fake.executed = append(fake.executed, input)
	return clusterterminal.CommandResult{
		Stdout: "out\n", Stderr: "err\n", ExitCode: 7, OutputBytes: 8,
	}, nil
}

func (fake *terminalCommanderFake) FinishCommandSession(
	_ context.Context,
	session clusterterminal.CommandSession,
) error {
	fake.closed = append(fake.closed, session)
	return nil
}

func testCommandSession(input clusterterminal.CommandSessionInput) clusterterminal.CommandSession {
	return clusterterminal.CommandSession{
		TerminalSessionID: input.IdempotencyKey,
		ClusterID:         input.ClusterID, Namespace: "zke-system", PodName: "terminal",
		PodUID: "pod-uid", Container: "terminal", UserID: input.UserID,
	}
}

func TestTerminalCommandProjectsCurrentPermissionsButNeverSecretAccess(t *testing.T) {
	commander := &terminalCommanderFake{}
	catalogue := New(Dependencies{
		Terminal: commander,
		Permissions: terminalPermissionResolverFake{allowed: map[rbac.Permission]bool{
			rbac.PermissionClusterTerminalExec:         true,
			rbac.PermissionClusterRead:                 true,
			rbac.PermissionClusterPodExec:              true,
			rbac.PermissionClusterSecretRead:           true,
			rbac.PermissionClusterSecretManage:         true,
			rbac.PermissionClusterAgentNamespaceManage: true,
		}},
	}, Config{})

	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolRunTerminalCommand, TurnID: "turn-1", ClusterID: "cluster", UserID: "user",
		IdempotencyKey: "aiops:key",
		Arguments:      json.RawMessage(`{"command":"kubectl exec api-0 -- id"}`),
	})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !result.Failed || len(commander.created) != 1 || len(commander.executed) != 1 ||
		commander.executed[0].Command != "kubectl exec api-0 -- id" ||
		!slices.Contains(commander.created[0].Permissions, string(rbac.PermissionClusterTerminalExec)) ||
		!slices.Contains(commander.created[0].Permissions, string(rbac.PermissionClusterPodExec)) ||
		!slices.Contains(commander.created[0].Permissions, string(rbac.PermissionClusterAgentNamespaceManage)) {
		t.Fatalf("result=%+v created=%+v executed=%+v", result, commander.created, commander.executed)
	}
	if slices.Contains(commander.created[0].Permissions, string(rbac.PermissionClusterSecretRead)) ||
		slices.Contains(commander.created[0].Permissions, string(rbac.PermissionClusterSecretManage)) {
		t.Fatalf("Secret permissions reached AIOps terminal: %v", commander.created[0].Permissions)
	}
	if _, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolRunTerminalCommand, TurnID: "turn-1", ClusterID: "cluster", UserID: "user",
		IdempotencyKey: "aiops:second",
		Arguments:      json.RawMessage(`{"command":"busybox id"}`),
	}); err != nil {
		t.Fatalf("second Invoke() error = %v", err)
	}
	if len(commander.created) != 1 || len(commander.executed) != 2 {
		t.Fatalf("same Turn created=%d executed=%d, want one Pod and two commands",
			len(commander.created), len(commander.executed))
	}
	if err := catalogue.CloseTurn(context.Background(), "turn-1"); err != nil {
		t.Fatalf("CloseTurn() error = %v", err)
	}
	if len(commander.closed) != 1 {
		t.Fatalf("closed sessions = %d, want 1", len(commander.closed))
	}

	var spec airuntime.ToolSpec
	ok := false
	for _, candidate := range catalogue.Specs() {
		if candidate.Name == toolRunTerminalCommand {
			spec, ok = candidate, true
			break
		}
	}
	if !ok || !spec.Sensitive || !spec.Mutating ||
		!slices.Equal(spec.Permissions, []rbac.Permission{rbac.PermissionClusterTerminalExec}) {
		t.Fatalf("terminal tool spec = %+v, found=%t", spec, ok)
	}
}

func TestTerminalCommandCancellationFollowsPermissionRevocation(t *testing.T) {
	permissions := &revokingPermissionResolverFake{}
	commander := &blockingTerminalCommanderFake{started: make(chan struct{})}
	catalogue := New(Dependencies{Terminal: commander, Permissions: permissions}, Config{
		TerminalRevalidate: time.Millisecond,
	})
	type outcome struct {
		result airuntime.ToolResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
			Name: toolRunTerminalCommand, TurnID: "turn-revoked", ClusterID: "cluster", UserID: "user",
			IdempotencyKey: "aiops:key",
			Arguments:      json.RawMessage(`{"command":"sleep 60"}`),
		})
		finished <- outcome{result: result, err: err}
	}()
	<-commander.started
	permissions.revoked.Store(true)
	select {
	case got := <-finished:
		if got.err != nil || !got.result.Failed ||
			!strings.Contains(got.result.Text, "权限重验失败") ||
			!strings.Contains(got.result.Text, "partial") || commander.closed.Load() != 1 {
			t.Fatalf("revoked terminal outcome = %+v error=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal command was not canceled after permission revocation")
	}
}

func TestTerminalTurnIsCleanedWhenPermissionIsRevokedBetweenCommands(t *testing.T) {
	permissions := &revokingPermissionResolverFake{}
	commander := &idleTerminalCommanderFake{done: make(chan struct{})}
	catalogue := New(Dependencies{Terminal: commander, Permissions: permissions}, Config{
		TerminalRevalidate: time.Millisecond,
	})
	invocation := airuntime.ToolInvocation{
		Name: toolRunTerminalCommand, TurnID: "turn-idle-revoked",
		ClusterID: "cluster", UserID: "user", IdempotencyKey: "aiops:first",
		Arguments: json.RawMessage(`{"command":"busybox id"}`),
	}
	if _, err := catalogue.Invoke(context.Background(), invocation); err != nil {
		t.Fatalf("first Invoke() error = %v", err)
	}
	permissions.revoked.Store(true)
	select {
	case <-commander.done:
	case <-time.After(time.Second):
		t.Fatal("idle Turn terminal was not cleaned after permission revocation")
	}

	invocation.IdempotencyKey = "aiops:second"
	invocation.Arguments = json.RawMessage(`{"command":"kubectl get pods"}`)
	result, err := catalogue.Invoke(context.Background(), invocation)
	if err != nil || !result.Failed || !strings.Contains(result.Text, "权限重验失败") {
		t.Fatalf("second Invoke() result=%+v error=%v", result, err)
	}
	if commander.created.Load() != 1 || commander.closed.Load() != 1 {
		t.Fatalf("created=%d closed=%d, want one Pod with one cleanup",
			commander.created.Load(), commander.closed.Load())
	}
	if err := catalogue.CloseTurn(context.Background(), invocation.TurnID); err != nil {
		t.Fatalf("CloseTurn() error = %v", err)
	}
	if commander.closed.Load() != 1 {
		t.Fatalf("CloseTurn repeated cleanup: %d", commander.closed.Load())
	}
}

func TestTerminalTurnDoesNotRetryAnUncertainCreate(t *testing.T) {
	commander := &failingTerminalCommanderFake{}
	catalogue := New(Dependencies{
		Terminal: commander,
		Permissions: terminalPermissionResolverFake{allowed: map[rbac.Permission]bool{
			rbac.PermissionClusterTerminalExec: true,
		}},
	}, Config{})
	invocation := airuntime.ToolInvocation{
		Name: toolRunTerminalCommand, TurnID: "turn-create-failed",
		ClusterID: "cluster", UserID: "user", IdempotencyKey: "aiops:first",
		Arguments: json.RawMessage(`{"command":"kubectl get pods"}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := catalogue.Invoke(context.Background(), invocation); !errors.Is(
			err, clusterterminal.ErrUpstreamFailure,
		) {
			t.Fatalf("Invoke() attempt %d error = %v, want original create failure", attempt+1, err)
		}
		invocation.IdempotencyKey = "aiops:retry"
	}
	if commander.created.Load() != 1 {
		t.Fatalf("create attempts = %d, want one for the entire Turn", commander.created.Load())
	}
	if err := catalogue.CloseTurn(context.Background(), invocation.TurnID); err != nil {
		t.Fatalf("CloseTurn() error = %v", err)
	}
}

func TestTerminalCommandRejectsOversizedInputBeforeCreatingPod(t *testing.T) {
	commander := &idleTerminalCommanderFake{done: make(chan struct{})}
	catalogue := New(Dependencies{
		Terminal: commander,
		Permissions: terminalPermissionResolverFake{allowed: map[rbac.Permission]bool{
			rbac.PermissionClusterTerminalExec: true,
		}},
	}, Config{})
	arguments, err := json.Marshal(terminalCommandArguments{
		Command: strings.Repeat("x", agentprotocol.MaxPodExecCommandBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolRunTerminalCommand, TurnID: "turn-oversized",
		ClusterID: "cluster", UserID: "user", Arguments: arguments,
	})
	if !errors.Is(err, airuntime.ErrInvalidInput) {
		t.Fatalf("Invoke() error = %v, want ErrInvalidInput", err)
	}
	if commander.created.Load() != 0 {
		t.Fatalf("oversized command created %d Pods", commander.created.Load())
	}
}
