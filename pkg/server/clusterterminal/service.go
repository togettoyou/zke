package clusterterminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/podexec"
)

var (
	ErrUnavailable         = errors.New("Cluster terminal is not configured")
	ErrIdempotencyConflict = errors.New("Cluster terminal idempotency conflict")
	ErrAgentNotConnected   = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported    = errors.New("Cluster Agent does not support Cluster terminal sessions")
	ErrClusterAccessDenied = errors.New("Kubernetes denied the Cluster terminal session")
	ErrUpstreamTimeout     = errors.New("Kubernetes Cluster terminal session timed out")
	ErrUpstreamFailure     = errors.New("Kubernetes Cluster terminal session failed")
)

type Requester interface {
	RequestTerminalSession(context.Context, string, *agentv1.TerminalSessionRequest, string) (*agentv1.TerminalSessionResponse, error)
}

type PodExecCreator interface {
	Create(podexec.CreateInput) (podexec.Session, error)
}

type Config struct {
	Image          string
	Namespace      string
	TTL            time.Duration
	ResolveRuntime func(context.Context) (RuntimeConfig, error)
}

type RuntimeConfig struct {
	Image     string
	Namespace string
}

type CreateInput struct {
	UserID, AuthSessionID, ClusterID, IdempotencyKey string
	Permissions                                      []string
	Columns, Rows                                    uint32
	Now                                              time.Time
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
	podExec     PodExecCreator
	config      Config
	mutex       sync.Mutex
	lifecycles  map[string]Lifecycle
	idempotency map[string]*idempotencyRecord
}

func NewService(requester Requester, podExec PodExecCreator, config Config) *Service {
	if config.TTL <= 0 || config.TTL > time.Hour {
		config.TTL = 15 * time.Minute
	}
	if config.Namespace == "" {
		config.Namespace = "zke-system"
	}
	return &Service{
		requester: requester, podExec: podExec, config: config,
		lifecycles: make(map[string]Lifecycle), idempotency: make(map[string]*idempotencyRecord),
	}
}

func (service *Service) Create(ctx context.Context, input CreateInput) (session podexec.Session, createErr error) {
	if service == nil || service.requester == nil || service.podExec == nil {
		return podexec.Session{}, ErrUnavailable
	}
	runtimeConfig, err := service.runtimeConfig(ctx)
	if err != nil {
		return podexec.Session{}, err
	}
	if runtimeConfig.Image == "" || runtimeConfig.Namespace == "" {
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
	defer func() { service.finishCreate(input, record, session, createErr) }()

	terminalID := deterministicTerminalID(input.UserID, input.ClusterID, runtimeConfig.Namespace, input.IdempotencyKey)
	response, requestErr := service.requester.RequestTerminalSession(ctx, input.ClusterID, &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: terminalID, UserId: input.UserID, Namespace: runtimeConfig.Namespace,
		Permissions: input.Permissions, TtlSeconds: uint64(service.config.TTL.Seconds()), Image: runtimeConfig.Image,
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

func (service *Service) runtimeConfig(ctx context.Context) (RuntimeConfig, error) {
	if service.config.ResolveRuntime != nil {
		return service.config.ResolveRuntime(ctx)
	}
	return RuntimeConfig{Image: service.config.Image, Namespace: service.config.Namespace}, nil
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

func (service *Service) finishCreate(input CreateInput, record *idempotencyRecord, session podexec.Session, err error) {
	key := input.UserID + "\x00" + input.IdempotencyKey
	service.mutex.Lock()
	record.session, record.err = session, err
	if err == nil {
		record.expiresAt = input.Now.Add(service.config.TTL)
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
	case errors.Is(err, agentconn.ErrTerminalSessionCapabilityMissing):
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
