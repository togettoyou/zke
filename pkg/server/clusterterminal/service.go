package clusterterminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/permissionname"
)

var (
	ErrUnavailable         = errors.New("Cluster terminal is not configured")
	ErrIdempotencyConflict = errors.New("Cluster terminal idempotency conflict")
	ErrAgentNotConnected   = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported    = errors.New("Cluster Agent does not support Cluster terminal sessions")
	ErrClusterAccessDenied = errors.New("Kubernetes denied the Cluster terminal session")
	ErrUpstreamTimeout     = errors.New("Kubernetes Cluster terminal session timed out")
	ErrUpstreamFailure     = errors.New("Kubernetes Cluster terminal session failed")
	ErrInvalidCommand      = errors.New("invalid Cluster terminal command")
)

type Requester interface {
	RequestTerminalSession(context.Context, string, *agentv1.TerminalSessionRequest, string) (*agentv1.TerminalSessionResponse, error)
}

type PodExecCreator interface {
	Create(podexec.CreateInput) (podexec.Session, error)
}

type CommandRequester interface {
	RequestTerminalCommand(
		context.Context,
		string,
		*agentv1.PodExecRequest,
		agentprotocol.PodExecPeer,
	) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error)
}

type Config struct {
	ResolveRuntime func(context.Context, string) (RuntimeConfig, error)
	// CommandMaxOutputBytes bounds raw stdout and stderr before they enter the
	// AIOps trajectory and model context. The catalogue applies its own rune
	// truncation afterwards; this earlier byte bound protects transport and
	// memory even for binary output.
	CommandMaxOutputBytes uint64
}

// RuntimeConfig is resolved once per session creation. The workload settings
// and the TTL come from the platform settings, so an operator's change applies
// to the next session without restarting the Server; Namespace comes from the
// Cluster scope.
type RuntimeConfig struct {
	// Workload is the Cluster Terminal's platform workload settings: the image
	// the session Pod runs and how much of the Cluster it may take. The budget
	// used to be constants inside the Agent, which made the one workload
	// running in the operator's own Cluster per session the one they could not
	// size.
	Workload  store.WorkloadSettings
	Namespace string
	TTL       time.Duration
}

type CreateInput struct {
	UserID, AuthSessionID, ClusterID, IdempotencyKey string
	Permissions                                      []string
	Columns, Rows                                    uint32
	Now                                              time.Time
}

type CommandSessionInput struct {
	UserID, ClusterID, IdempotencyKey string
	Permissions                       []string
}

// CommandSession identifies one Agent-owned terminal Pod that is private to a
// single AIOps turn. It contains no Kubernetes credential; the command
// container reaches the API only through the Pod-local credential proxy.
type CommandSession struct {
	TerminalSessionID string
	ClusterID         string
	Namespace         string
	PodName           string
	PodUID            string
	Container         string
	UserID            string
}

type CommandInput struct {
	Session CommandSession
	Command string
}

type CommandResult struct {
	Stdout             string
	Stderr             string
	ExitCode           int32
	OutputBytes        uint64
	OutputLimitReached bool
}

type Lifecycle struct {
	TerminalSessionID string
	ClusterID         string
	Namespace         string
	UserID            string
	Permissions       []string
}

type idempotencyRecord struct {
	fingerprint [sha256.Size]byte
	ready       chan struct{}
	session     podexec.Session
	err         error
	expiresAt   time.Time
}

const terminalCleanupTimeout = 15 * time.Second

type Service struct {
	requester   Requester
	commands    CommandRequester
	podExec     PodExecCreator
	config      Config
	mutex       sync.Mutex
	lifecycles  map[string]Lifecycle
	idempotency map[string]*idempotencyRecord
}

func NewService(requester Requester, podExec PodExecCreator, config Config) *Service {
	if config.CommandMaxOutputBytes == 0 {
		config.CommandMaxOutputBytes = 32 * 1024
	}
	commands, _ := requester.(CommandRequester)
	return &Service{
		requester: requester, commands: commands, podExec: podExec, config: config,
		lifecycles: make(map[string]Lifecycle), idempotency: make(map[string]*idempotencyRecord),
	}
}

// CreateCommandSession creates one permission-projected terminal Pod for an
// AIOps turn. Commands never receive a kubeconfig or ServiceAccount token:
// kubectl goes through the Agent-created localhost credential proxy, so every
// Kubernetes API operation — including pods/exec — is still decided by the
// target Cluster.
func (service *Service) CreateCommandSession(
	ctx context.Context,
	input CommandSessionInput,
) (session CommandSession, createErr error) {
	if service == nil || service.requester == nil || service.commands == nil ||
		strings.TrimSpace(input.UserID) == "" || strings.TrimSpace(input.ClusterID) == "" ||
		strings.TrimSpace(input.IdempotencyKey) == "" ||
		!terminalPermissionHeld(input.Permissions, permissionname.ClusterTerminalExec) {
		return CommandSession{}, ErrInvalidCommand
	}
	runtimeConfig, err := service.runtimeConfig(ctx, input.ClusterID)
	if err != nil {
		return CommandSession{}, err
	}
	if runtimeConfig.Workload.Image == "" || runtimeConfig.Workload.ImagePullPolicy == "" ||
		runtimeConfig.Namespace == "" || runtimeConfig.TTL <= 0 {
		return CommandSession{}, ErrUnavailable
	}

	terminalID := deterministicTerminalID(
		input.UserID, input.ClusterID, runtimeConfig.Namespace, "aiops", input.IdempotencyKey,
	)
	lifecycle := Lifecycle{
		TerminalSessionID: terminalID, ClusterID: input.ClusterID,
		Namespace: runtimeConfig.Namespace, UserID: input.UserID,
	}
	response, err := service.requester.RequestTerminalSession(ctx, input.ClusterID, &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: terminalID, UserId: input.UserID, Namespace: runtimeConfig.Namespace,
		Permissions: input.Permissions, TtlSeconds: uint64(runtimeConfig.TTL.Seconds()),
		Image:           runtimeConfig.Workload.Image,
		ImagePullPolicy: runtimeConfig.Workload.ImagePullPolicy,
		CpuRequest:      runtimeConfig.Workload.CPURequest,
		MemoryRequest:   runtimeConfig.Workload.MemoryRequest,
		CpuLimit:        runtimeConfig.Workload.CPULimit,
		MemoryLimit:     runtimeConfig.Workload.MemoryLimit,
		CredentialProxy: true,
	}, terminalIdempotencyKey(input.IdempotencyKey, "ai-command-create"))
	if err != nil {
		// The Agent may have committed CREATE before its response was lost. The
		// deterministic identity lets us compensate without knowing the Pod UID;
		// the Turn is also tombstoned by aitools so it cannot race this cleanup by
		// recreating the same resources with a newer permission snapshot.
		_ = service.deleteDetached(ctx, lifecycle)
		return CommandSession{}, terminalRequestError(err)
	}
	if err := terminalResponseError(response); err != nil {
		_ = service.deleteDetached(ctx, lifecycle)
		return CommandSession{}, err
	}
	lifecycle.Namespace = response.GetNamespace()
	if err := ctx.Err(); err != nil {
		_ = service.deleteDetached(ctx, lifecycle)
		return CommandSession{}, terminalRequestError(err)
	}
	return CommandSession{
		TerminalSessionID: terminalID,
		ClusterID:         input.ClusterID,
		Namespace:         response.GetNamespace(),
		PodName:           response.GetPodName(),
		PodUID:            response.GetPodUid(),
		Container:         response.GetContainer(),
		UserID:            input.UserID,
	}, nil
}

// ExecuteCommand runs one non-interactive shell command in an existing
// turn-scoped terminal Pod. The caller owns permission revalidation and the
// session lifetime; a non-zero shell exit is a structured result, not a broken
// session.
func (service *Service) ExecuteCommand(
	ctx context.Context,
	input CommandInput,
) (result CommandResult, executeErr error) {
	if service == nil || service.commands == nil ||
		strings.TrimSpace(input.Session.TerminalSessionID) == "" ||
		strings.TrimSpace(input.Session.ClusterID) == "" ||
		strings.TrimSpace(input.Session.Namespace) == "" ||
		strings.TrimSpace(input.Session.PodName) == "" ||
		strings.TrimSpace(input.Session.PodUID) == "" ||
		strings.TrimSpace(input.Session.Container) == "" ||
		strings.TrimSpace(input.Command) == "" ||
		len(input.Command) > agentprotocol.MaxPodExecCommandBytes {
		return CommandResult{}, ErrInvalidCommand
	}

	peer := &commandPeer{}
	execResponse, exit, err := service.commands.RequestTerminalCommand(
		ctx,
		input.Session.ClusterID,
		&agentv1.PodExecRequest{
			Namespace: input.Session.Namespace, PodName: input.Session.PodName,
			PodUid: input.Session.PodUID, Container: input.Session.Container,
			Tty: false, Columns: 80, Rows: 24, MaxInputBytes: 1,
			MaxOutputBytes: service.config.CommandMaxOutputBytes,
			Command:        []string{"/bin/sh", "-c", input.Command},
		},
		peer,
	)
	result.Stdout = strings.ToValidUTF8(peer.stdout.String(), "�")
	result.Stderr = strings.ToValidUTF8(peer.stderr.String(), "�")
	if err != nil {
		return result, terminalCommandRequestError(err)
	}
	if err := terminalCommandResponseError(execResponse); err != nil {
		return result, err
	}
	if exit == nil || exit.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return result, ErrUpstreamFailure
	}
	result.ExitCode = exit.GetExitCode()
	result.OutputBytes = exit.GetOutputBytes()
	result.OutputLimitReached = exit.GetOutputLimitReached()
	if exit.GetResult() == agentv1.ResultCode_RESULT_CODE_OK || result.OutputLimitReached {
		return result, nil
	}
	return result, terminalCommandResultError(exit.GetResult(), exit.GetReason())
}

// FinishCommandSession removes the terminal Pod and all temporary RBAC. It is
// safe to call with a cancelled turn parent because deletion is detached and
// bounded; the Agent session TTL remains the final process-crash fallback.
func (service *Service) FinishCommandSession(ctx context.Context, session CommandSession) error {
	if service == nil || service.requester == nil ||
		strings.TrimSpace(session.TerminalSessionID) == "" {
		return nil
	}
	return service.deleteDetached(ctx, Lifecycle{
		TerminalSessionID: session.TerminalSessionID,
		ClusterID:         session.ClusterID,
		Namespace:         session.Namespace,
		UserID:            session.UserID,
	})
}

type commandPeer struct {
	closed bool
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (peer *commandPeer) Receive(ctx context.Context) (*agentv1.PodExecFrame, error) {
	if !peer.closed {
		peer.closed = true
		return &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_CloseInput{
			CloseInput: &agentv1.PodExecCloseInput{},
		}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (peer *commandPeer) Send(_ context.Context, frame *agentv1.PodExecFrame) error {
	output := frame.GetOutput()
	if output == nil {
		return nil
	}
	var destination io.Writer
	switch output.GetStream() {
	case agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT:
		destination = &peer.stdout
	case agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDERR:
		destination = &peer.stderr
	default:
		return ErrUpstreamFailure
	}
	_, err := destination.Write(output.GetData())
	return err
}

func terminalPermissionHeld(permissions []string, want string) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}
	return false
}

func terminalCommandRequestError(err error) error {
	switch {
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrTerminalCommandCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrPodExecRequestExhausted):
		return fmt.Errorf("%w: terminal command capacity exhausted", ErrUpstreamFailure)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrUpstreamTimeout, err)
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func terminalCommandResponseError(response *agentv1.PodExecResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrUpstreamFailure
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	return terminalCommandResultError(response.GetResult(), response.GetReason())
}

func terminalCommandResultError(result agentv1.ResultCode, reason string) error {
	switch result {
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED,
		agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return fmt.Errorf("%w: %s", ErrUpstreamTimeout, reason)
	default:
		return fmt.Errorf("%w: %s", ErrUpstreamFailure, reason)
	}
}

func (service *Service) Create(ctx context.Context, input CreateInput) (session podexec.Session, createErr error) {
	if service == nil || service.requester == nil || service.podExec == nil {
		return podexec.Session{}, ErrUnavailable
	}
	runtimeConfig, err := service.runtimeConfig(ctx, input.ClusterID)
	if err != nil {
		return podexec.Session{}, err
	}
	if runtimeConfig.Workload.Image == "" || runtimeConfig.Workload.ImagePullPolicy == "" ||
		runtimeConfig.Namespace == "" || runtimeConfig.TTL <= 0 {
		return podexec.Session{}, ErrUnavailable
	}
	record, owner, beginErr := service.beginCreate(input)
	if beginErr != nil {
		return podexec.Session{}, beginErr
	}
	if !owner {
		select {
		case <-ctx.Done():
			return podexec.Session{}, ctx.Err()
		case <-record.ready:
			return record.session, record.err
		}
	}
	defer func() { service.finishCreate(input, record, runtimeConfig.TTL, session, createErr) }()

	terminalID := deterministicTerminalID(input.UserID, input.ClusterID, runtimeConfig.Namespace, input.IdempotencyKey)
	response, requestErr := service.requester.RequestTerminalSession(ctx, input.ClusterID, &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: terminalID, UserId: input.UserID, Namespace: runtimeConfig.Namespace,
		Permissions: input.Permissions, TtlSeconds: uint64(runtimeConfig.TTL.Seconds()),
		Image:           runtimeConfig.Workload.Image,
		ImagePullPolicy: runtimeConfig.Workload.ImagePullPolicy,
		CpuRequest:      runtimeConfig.Workload.CPURequest,
		MemoryRequest:   runtimeConfig.Workload.MemoryRequest,
		CpuLimit:        runtimeConfig.Workload.CPULimit,
		MemoryLimit:     runtimeConfig.Workload.MemoryLimit,
	}, terminalIdempotencyKey(input.IdempotencyKey, "create"))
	if requestErr != nil {
		createErr = terminalRequestError(requestErr)
		return podexec.Session{}, createErr
	}
	if responseErr := terminalResponseError(response); responseErr != nil {
		createErr = responseErr
		return podexec.Session{}, createErr
	}
	lifecycle := Lifecycle{TerminalSessionID: terminalID, ClusterID: input.ClusterID,
		Namespace: response.GetNamespace(), UserID: input.UserID}
	if err := ctx.Err(); err != nil {
		createErr = terminalRequestError(err)
		_ = service.deleteDetached(ctx, lifecycle)
		return podexec.Session{}, createErr
	}
	session, createErr = service.podExec.Create(podexec.CreateInput{
		UserID: input.UserID, AuthSessionID: input.AuthSessionID, IdempotencyKey: input.IdempotencyKey,
		ClusterID: input.ClusterID, Namespace: response.GetNamespace(), PodName: response.GetPodName(), PodUID: response.GetPodUid(),
		Container: response.GetContainer(), Columns: input.Columns, Rows: input.Rows, Confirm: true, Now: input.Now,
	})
	if createErr != nil {
		_ = service.deleteDetached(ctx, lifecycle)
		return podexec.Session{}, createErr
	}
	if err := ctx.Err(); err != nil {
		createErr = terminalRequestError(err)
		_ = service.deleteDetached(ctx, lifecycle)
		return podexec.Session{}, createErr
	}
	service.mutex.Lock()
	lifecycle.Permissions = append([]string(nil), input.Permissions...)
	service.lifecycles[session.ID] = lifecycle
	service.mutex.Unlock()
	return session, nil
}

func (service *Service) runtimeConfig(ctx context.Context, clusterID string) (RuntimeConfig, error) {
	if service.config.ResolveRuntime == nil {
		return RuntimeConfig{}, ErrUnavailable
	}
	return service.config.ResolveRuntime(ctx, clusterID)
}

func (service *Service) beginCreate(input CreateInput) (*idempotencyRecord, bool, error) {
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d",
		input.AuthSessionID, input.ClusterID, input.Columns, input.Rows)))
	key := input.UserID + "\x00" + input.IdempotencyKey
	service.mutex.Lock()
	defer service.mutex.Unlock()
	for existingKey, existing := range service.idempotency {
		if !existing.expiresAt.IsZero() && !input.Now.Before(existing.expiresAt) {
			delete(service.idempotency, existingKey)
			delete(service.lifecycles, existing.session.ID)
		}
	}
	if existing, exists := service.idempotency[key]; exists {
		if existing.fingerprint != fingerprint {
			return nil, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	record := &idempotencyRecord{fingerprint: fingerprint, ready: make(chan struct{})}
	service.idempotency[key] = record
	return record, true, nil
}

// finishCreate takes the TTL that this session was created with rather than
// reading it back from the platform settings: the idempotency window must
// match the lifetime the Agent was actually told to enforce, even if an
// operator changes the setting midway through the creation.
func (service *Service) finishCreate(input CreateInput, record *idempotencyRecord, ttl time.Duration, session podexec.Session, err error) {
	key := input.UserID + "\x00" + input.IdempotencyKey
	service.mutex.Lock()
	record.session, record.err = session, err
	if err == nil {
		record.expiresAt = input.Now.Add(ttl)
	}
	if err != nil && service.idempotency[key] == record {
		delete(service.idempotency, key)
	}
	close(record.ready)
	service.mutex.Unlock()
}

func deterministicTerminalID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	value := hex.EncodeToString(sum[:16])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:32]
}

func (service *Service) Permissions(podExecSessionID string) []string {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return append([]string(nil), service.lifecycles[podExecSessionID].Permissions...)
}

func (service *Service) Finish(ctx context.Context, podExecSessionID string) error {
	service.mutex.Lock()
	lifecycle, exists := service.lifecycles[podExecSessionID]
	if exists {
		delete(service.lifecycles, podExecSessionID)
	}
	service.mutex.Unlock()
	if !exists {
		return nil
	}
	return service.delete(ctx, lifecycle)
}

func (service *Service) delete(ctx context.Context, lifecycle Lifecycle) error {
	response, err := service.requester.RequestTerminalSession(ctx, lifecycle.ClusterID, &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE,
		SessionId: lifecycle.TerminalSessionID, UserId: lifecycle.UserID, Namespace: lifecycle.Namespace,
	}, terminalIdempotencyKey(lifecycle.TerminalSessionID, "delete"))
	if err != nil {
		return terminalRequestError(err)
	}
	return terminalResponseError(response)
}

func (service *Service) deleteDetached(parent context.Context, lifecycle Lifecycle) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), terminalCleanupTimeout)
	defer cancel()
	return service.delete(ctx, lifecycle)
}

func terminalIdempotencyKey(value, action string) string {
	return fmt.Sprintf("terminal-%s-%x", action, sha256.Sum256([]byte(value)))
}

func terminalRequestError(err error) error {
	switch {
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrTerminalSessionCapabilityMissing),
		errors.Is(err, agentconn.ErrTerminalCommandCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrUpstreamTimeout, err)
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func terminalResponseError(response *agentv1.TerminalSessionResponse) error {
	if response == nil {
		return ErrUpstreamFailure
	}
	switch response.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_OK:
		return nil
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN, agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return fmt.Errorf("%w: %s", ErrUpstreamTimeout, response.GetReason())
	default:
		return fmt.Errorf("%w: %s", ErrUpstreamFailure, response.GetReason())
	}
}
