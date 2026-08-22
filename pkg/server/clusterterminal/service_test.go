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
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/permissionname"
)

type terminalRequesterFake struct {
	requests  []*agentv1.TerminalSessionRequest
	execs     []*agentv1.PodExecRequest
	onRequest func(*agentv1.TerminalSessionRequest)
	createErr error
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
	if request.GetAction() == agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE &&
		fake.createErr != nil {
		return nil, fake.createErr
	}
	return &agentv1.TerminalSessionResponse{
		Result:    agentv1.ResultCode_RESULT_CODE_OK,
		Namespace: request.GetNamespace(),
		PodName:   "zke-terminal-test",
		PodUid:    "pod-uid",
		Container: "terminal",
	}, nil
}

func (fake *terminalRequesterFake) RequestTerminalCommand(
	ctx context.Context,
	_ string,
	request *agentv1.PodExecRequest,
	peer agentprotocol.PodExecPeer,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	fake.execs = append(fake.execs, request)
	if err := peer.Send(ctx, &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Output{
		Output: &agentv1.PodExecOutput{Stream: agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT, Data: []byte("pod/api-0\n")},
	}}); err != nil {
		return nil, nil, err
	}
	if err := peer.Send(ctx, &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Output{
		Output: &agentv1.PodExecOutput{Stream: agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDERR, Data: []byte("warning\n")},
	}}); err != nil {
		return nil, nil, err
	}
	return &agentv1.PodExecResponse{
		Result: agentv1.ResultCode_RESULT_CODE_OK, PodUid: request.GetPodUid(), Container: request.GetContainer(),
	}, &agentv1.PodExecExit{Result: agentv1.ResultCode_RESULT_CODE_OK, ExitCode: 0, OutputBytes: 18}, nil
}

type podExecCreatorFake struct {
	input podexec.CreateInput
}

const testTerminalTTL = 10 * time.Minute

func fixedTerminalRuntime(image, imagePullPolicy, namespace string) func(context.Context, string) (RuntimeConfig, error) {
	return func(context.Context, string) (RuntimeConfig, error) {
		return RuntimeConfig{
			Workload:  store.WorkloadSettings{Image: image, ImagePullPolicy: imagePullPolicy},
			Namespace: namespace, TTL: testTerminalTTL,
		}, nil
	}
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
	service := NewService(requester, podExec, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "Never", "cluster-agent"),
	})
	wantPermissions := []string{permissionname.ClusterRead, permissionname.ClusterTerminalExec}

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
	if request.GetNamespace() != "cluster-agent" || podExec.input.Namespace != "cluster-agent" {
		t.Fatalf("terminal namespace request=%q exec=%q, want cluster-agent", request.GetNamespace(), podExec.input.Namespace)
	}
	if request.GetImagePullPolicy() != "Never" {
		t.Fatalf("image pull policy = %q, want Never", request.GetImagePullPolicy())
	}
	if slices.Contains(request.GetPermissions(), permissionname.ClusterSecretRead) ||
		slices.Contains(request.GetPermissions(), permissionname.ClusterSecretManage) {
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

func TestCommandSessionReusesOneCredentialProxyPodUntilExplicitFinish(t *testing.T) {
	requester := &terminalRequesterFake{}
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "Never", "zke-system"),
	})
	permissions := []string{permissionname.ClusterTerminalExec, permissionname.ClusterRead, permissionname.ClusterPodExec}
	session, err := service.CreateCommandSession(context.Background(), CommandSessionInput{
		UserID: "user", ClusterID: "cluster", IdempotencyKey: "aiops-call",
		Permissions: permissions,
	})
	if err != nil {
		t.Fatalf("CreateCommandSession() error = %v", err)
	}
	for _, command := range []string{"kubectl get pods", "busybox id"} {
		result, executeErr := service.ExecuteCommand(context.Background(), CommandInput{
			Session: session, Command: command,
		})
		if executeErr != nil {
			t.Fatalf("ExecuteCommand(%q) error = %v", command, executeErr)
		}
		if result.ExitCode != 0 || result.Stdout != "pod/api-0\n" || result.Stderr != "warning\n" {
			t.Fatalf("ExecuteCommand(%q) result = %+v", command, result)
		}
	}
	if len(requester.requests) != 1 ||
		requester.requests[0].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE ||
		!requester.requests[0].GetCredentialProxy() ||
		!slices.Equal(requester.requests[0].GetPermissions(), permissions) {
		t.Fatalf("terminal lifecycle requests = %+v", requester.requests)
	}
	if len(requester.execs) != 2 || requester.execs[0].GetTty() || requester.execs[1].GetTty() ||
		!slices.Equal(requester.execs[0].GetCommand(), []string{"/bin/sh", "-c", "kubectl get pods"}) ||
		!slices.Equal(requester.execs[1].GetCommand(), []string{"/bin/sh", "-c", "busybox id"}) {
		t.Fatalf("terminal command requests = %+v", requester.execs)
	}
	if err := service.FinishCommandSession(context.Background(), session); err != nil {
		t.Fatalf("FinishCommandSession() error = %v", err)
	}
	if len(requester.requests) != 2 ||
		requester.requests[1].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE {
		t.Fatalf("terminal lifecycle after finish = %+v", requester.requests)
	}
}

func TestCreateCommandSessionRequiresTerminalPermissionInProjectedSnapshot(t *testing.T) {
	requester := &terminalRequesterFake{}
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "Never", "zke-system"),
	})
	_, err := service.CreateCommandSession(context.Background(), CommandSessionInput{
		UserID: "user", ClusterID: "cluster", IdempotencyKey: "aiops-call",
		Permissions: []string{permissionname.ClusterRead},
	})
	if !errors.Is(err, ErrInvalidCommand) || len(requester.requests) != 0 {
		t.Fatalf("CreateCommandSession() error = %v requests=%d, want local rejection", err, len(requester.requests))
	}
}

func TestCreateCommandSessionCancellationCleansAgentResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requester := &terminalRequesterFake{}
	requester.onRequest = func(request *agentv1.TerminalSessionRequest) {
		if request.GetAction() == agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE {
			cancel()
		}
	}
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "Never", "zke-system"),
	})
	_, err := service.CreateCommandSession(ctx, CommandSessionInput{
		UserID: "user", ClusterID: "cluster", IdempotencyKey: "aiops-turn",
		Permissions: []string{permissionname.ClusterTerminalExec},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateCommandSession() error = %v, want context cancellation", err)
	}
	if len(requester.requests) != 2 ||
		requester.requests[1].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE {
		t.Fatalf("requests = %+v, want create followed by detached delete", requester.requests)
	}
}

func TestCreateCommandSessionLostResponseCompensatesByDeterministicDelete(t *testing.T) {
	requester := &terminalRequesterFake{createErr: agentconn.ErrAgentNotConnected}
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "Never", "zke-system"),
	})
	_, err := service.CreateCommandSession(context.Background(), CommandSessionInput{
		UserID: "user", ClusterID: "cluster", IdempotencyKey: "aiops-turn",
		Permissions: []string{permissionname.ClusterTerminalExec},
	})
	if !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("CreateCommandSession() error = %v, want ErrAgentNotConnected", err)
	}
	if len(requester.requests) != 2 ||
		requester.requests[0].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE ||
		requester.requests[1].GetAction() != agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE ||
		requester.requests[0].GetSessionId() != requester.requests[1].GetSessionId() {
		t.Fatalf("requests = %+v, want CREATE followed by same-session DELETE", requester.requests)
	}
}

func TestCreateResolvesLatestRuntimeConfiguration(t *testing.T) {
	requester := &terminalRequesterFake{}
	image, imagePullPolicy, cpuRequest := "terminal:v1", "IfNotPresent", "25m"
	ttl := 5 * time.Minute
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: func(context.Context, string) (RuntimeConfig, error) {
			return RuntimeConfig{
				Workload: store.WorkloadSettings{
					Image: image, ImagePullPolicy: imagePullPolicy,
					CPURequest: cpuRequest, MemoryLimit: "512Mi",
				},
				Namespace: "zke-system", TTL: ttl,
			}, nil
		},
	})
	create := func(key string, now time.Time) {
		t.Helper()
		if _, err := service.Create(context.Background(), CreateInput{
			UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
			IdempotencyKey: key, Permissions: []string{permissionname.ClusterTerminalExec},
			Columns: 120, Rows: 36, Now: now,
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	create("first", time.Unix(100, 0))
	image = "terminal:v2"
	imagePullPolicy = "Always"
	cpuRequest = "100m"
	ttl = 20 * time.Minute
	create("second", time.Unix(101, 0))
	if len(requester.requests) != 2 || requester.requests[0].GetImage() != "terminal:v1" ||
		requester.requests[0].GetImagePullPolicy() != "IfNotPresent" ||
		requester.requests[1].GetImage() != "terminal:v2" || requester.requests[1].GetImagePullPolicy() != "Always" {
		t.Fatalf("terminal runtime settings = %+v, want v1/IfNotPresent then v2/Always", requester.requests)
	}
	// The budget is platform settings state as much as the image is: an
	// operator resizing the session Pod must reach the next session without
	// the Agent holding a constant of its own.
	if requester.requests[0].GetCpuRequest() != "25m" ||
		requester.requests[1].GetCpuRequest() != "100m" ||
		requester.requests[1].GetMemoryLimit() != "512Mi" {
		t.Fatalf("terminal budgets = %+v, want 25m then 100m/512Mi", requester.requests)
	}
	// The session lifetime is platform settings state now, so a change between
	// two sessions must reach the Agent without restarting the Server.
	if requester.requests[0].GetTtlSeconds() != 300 || requester.requests[1].GetTtlSeconds() != 1200 {
		t.Fatalf("terminal TTLs = %ds then %ds, want 300 then 1200",
			requester.requests[0].GetTtlSeconds(), requester.requests[1].GetTtlSeconds())
	}
}

// A platform settings row that somehow carries no lifetime must fail the
// session rather than fall back to a silent built-in default.
func TestCreateRejectsRuntimeWithoutSessionTTL(t *testing.T) {
	service := NewService(&terminalRequesterFake{}, &podExecCreatorFake{}, Config{
		ResolveRuntime: func(context.Context, string) (RuntimeConfig, error) {
			return RuntimeConfig{
				Workload:  store.WorkloadSettings{Image: "terminal:test", ImagePullPolicy: "Never"},
				Namespace: "zke-system",
			}, nil
		},
	})
	_, err := service.Create(context.Background(), CreateInput{
		UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: []string{permissionname.ClusterTerminalExec},
		Columns: 120, Rows: 36, Now: time.Unix(100, 0),
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create() error = %v, want ErrUnavailable", err)
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
	if capabilityErr := terminalRequestError(agentconn.ErrTerminalCommandCapabilityMissing); !errors.Is(
		capabilityErr, ErrAgentUnsupported,
	) {
		t.Fatalf("terminalRequestError(command capability) = %v, want ErrAgentUnsupported", capabilityErr)
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
	service := NewService(requester, podExec, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "IfNotPresent", "cluster-agent"),
	})

	_, err := service.Create(ctx, CreateInput{
		UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: []string{permissionname.ClusterTerminalExec},
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
	service := NewService(requester, &podExecCreatorFake{}, Config{
		ResolveRuntime: fixedTerminalRuntime("terminal:test", "IfNotPresent", "cluster-agent"),
	})
	input := CreateInput{UserID: "user", AuthSessionID: "auth-session", ClusterID: "cluster",
		IdempotencyKey: "request-key", Permissions: []string{permissionname.ClusterTerminalExec}, Columns: 120, Rows: 36, Now: time.Unix(100, 0)}
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
