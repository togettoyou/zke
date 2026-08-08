package podportforward

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrInvalidInput           = errors.New("invalid Kubernetes Pod port-forward input")
	ErrConfirmationRequired   = errors.New("Kubernetes Pod port-forward confirmation is required")
	ErrIdempotencyConflict    = errors.New("Pod port-forward idempotency conflict")
	ErrSessionCapacity        = errors.New("Pod port-forward pending session capacity is exhausted")
	ErrSessionNotFound        = errors.New("Pod port-forward session was not found")
	ErrSessionExpired         = errors.New("Pod port-forward session expired")
	ErrSessionBindingMismatch = errors.New("Pod port-forward session binding does not match")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support Pod Port Forward")
	ErrRequestCapacity        = errors.New("Pod Port Forward request capacity is exhausted")
	ErrPodNotFound            = errors.New("Kubernetes Pod not found")
	ErrPodReplaced            = errors.New("Kubernetes Pod identity changed")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes Pod Port Forward request timed out")
	ErrByteLimit              = errors.New("Pod port-forward byte limit reached")
	ErrUpstreamFailure        = errors.New("Kubernetes Pod Port Forward request failed")
	ErrInvalidResponse        = errors.New("invalid Agent Pod Port Forward response")
)

type Requester interface {
	RequestPodPortForward(
		context.Context,
		string,
		*agentv1.PodPortForwardRequest,
		agentprotocol.PodPortForwardPeer,
	) (*agentv1.PodPortForwardResponse, *agentv1.PodPortForwardExit, error)
}

type Config struct {
	SessionTTL     time.Duration
	MaxPending     int
	MaxClientBytes uint64
	MaxPodBytes    uint64
}

type CreateInput struct {
	UserID, AuthSessionID, IdempotencyKey string
	ClusterID, Namespace, PodName, PodUID string
	Port                                  uint32
	Confirm                               bool
	Now                                   time.Time
}

type ConsumeInput struct {
	ID, UserID, AuthSessionID     string
	ClusterID, Namespace, PodName string
	Now                           time.Time
}

type Session struct {
	ID, UserID, AuthSessionID             string
	ClusterID, Namespace, PodName, PodUID string
	Port                                  uint32
	CreatedAt, ExpiresAt                  time.Time
}

type Result struct {
	ClientBytes, PodBytes               uint64
	ClientLimitReached, PodLimitReached bool
}

type idempotencyRecord struct {
	fingerprint [sha256.Size]byte
	session     Session
}

type Service struct {
	requester   Requester
	config      Config
	mutex       sync.Mutex
	pending     map[string]Session
	idempotency map[string]idempotencyRecord
}

func NewService(requester Requester, config Config) *Service {
	if config.SessionTTL <= 0 {
		config.SessionTTL = 30 * time.Second
	}
	if config.MaxPending <= 0 {
		config.MaxPending = 1024
	}
	if config.MaxClientBytes == 0 {
		config.MaxClientBytes = agentprotocol.DefaultMaxPodPortForwardClientBytes
	}
	if config.MaxPodBytes == 0 {
		config.MaxPodBytes = agentprotocol.DefaultMaxPodPortForwardPodBytes
	}
	return &Service{requester: requester, config: config, pending: map[string]Session{}, idempotency: map[string]idempotencyRecord{}}
}

func (service *Service) Create(input CreateInput) (Session, error) {
	if service == nil || validateCreateInput(input) != nil {
		return Session{}, ErrInvalidInput
	}
	if !input.Confirm {
		return Session{}, ErrConfirmationRequired
	}
	fingerprint, err := createFingerprint(input)
	if err != nil {
		return Session{}, ErrInvalidInput
	}
	key := input.UserID + "\x00" + input.IdempotencyKey
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.removeExpiredLocked(input.Now)
	if record, exists := service.idempotency[key]; exists {
		if record.fingerprint != fingerprint {
			return Session{}, ErrIdempotencyConflict
		}
		return record.session, nil
	}
	if len(service.pending) >= service.config.MaxPending || len(service.idempotency) >= service.config.MaxPending {
		return Session{}, ErrSessionCapacity
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Session{}, fmt.Errorf("generate Pod port-forward session identifier: %w", err)
	}
	session := Session{ID: id, UserID: input.UserID, AuthSessionID: input.AuthSessionID, ClusterID: input.ClusterID,
		Namespace: input.Namespace, PodName: input.PodName, PodUID: input.PodUID, Port: input.Port,
		CreatedAt: input.Now, ExpiresAt: input.Now.Add(service.config.SessionTTL)}
	service.pending[id] = session
	service.idempotency[key] = idempotencyRecord{fingerprint: fingerprint, session: session}
	return session, nil
}

func (service *Service) Consume(input ConsumeInput) (Session, error) {
	if service == nil || !validation.IsUUID(input.ID) || !validation.IsUUID(input.UserID) ||
		!validation.IsUUID(input.AuthSessionID) || !validation.IsUUID(input.ClusterID) || input.Now.IsZero() ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 || len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 {
		return Session{}, ErrInvalidInput
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	session, exists := service.pending[input.ID]
	if !exists {
		return Session{}, ErrSessionNotFound
	}
	delete(service.pending, input.ID)
	if !input.Now.Before(session.ExpiresAt) {
		return Session{}, ErrSessionExpired
	}
	if session.UserID != input.UserID || session.AuthSessionID != input.AuthSessionID ||
		session.ClusterID != input.ClusterID || session.Namespace != input.Namespace || session.PodName != input.PodName {
		return Session{}, ErrSessionBindingMismatch
	}
	return session, nil
}

func (service *Service) Run(ctx context.Context, session Session, peer agentprotocol.PodPortForwardPeer) (Result, error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if ctx == nil || peer == nil || !validation.IsUUID(session.ID) {
		return Result{}, ErrInvalidInput
	}
	response, exit, err := service.requester.RequestPodPortForward(ctx, session.ClusterID,
		&agentv1.PodPortForwardRequest{Namespace: session.Namespace, PodName: session.PodName, PodUid: session.PodUID,
			Port: session.Port, MaxClientBytes: service.config.MaxClientBytes, MaxPodBytes: service.config.MaxPodBytes}, peer)
	if err != nil {
		return Result{}, requestError(ctx, err)
	}
	if err := responseError(response); err != nil {
		return Result{}, err
	}
	if exit == nil || exit.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return Result{}, ErrInvalidResponse
	}
	result := Result{ClientBytes: exit.GetClientBytes(), PodBytes: exit.GetPodBytes(), ClientLimitReached: exit.GetClientLimitReached(), PodLimitReached: exit.GetPodLimitReached()}
	if exit.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return result, nil
	}
	if result.ClientLimitReached || result.PodLimitReached {
		return result, ErrByteLimit
	}
	return result, resultError(exit.GetResult(), exit.GetReason())
}

func validateCreateInput(input CreateInput) error {
	if !validation.IsUUID(input.UserID) || !validation.IsUUID(input.AuthSessionID) || !validation.IsIdempotencyKey(input.IdempotencyKey) ||
		!validation.IsUUID(input.ClusterID) || input.Now.IsZero() || len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 || input.PodUID == "" || len(input.PodUID) > 256 ||
		strings.TrimSpace(input.PodUID) != input.PodUID || input.Port == 0 || input.Port > 65535 {
		return ErrInvalidInput
	}
	return nil
}

func createFingerprint(input CreateInput) ([sha256.Size]byte, error) {
	data, err := json.Marshal([]any{input.AuthSessionID, input.ClusterID, input.Namespace, input.PodName, input.PodUID, input.Port})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (service *Service) removeExpiredLocked(now time.Time) {
	for id, session := range service.pending {
		if !now.Before(session.ExpiresAt) {
			delete(service.pending, id)
		}
	}
	for key, record := range service.idempotency {
		if !now.Before(record.session.ExpiresAt) {
			delete(service.idempotency, key)
		}
	}
}

func requestError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return ErrClusterTimeout
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrPodPortForwardCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrPodPortForwardRequestExhausted):
		return ErrRequestCapacity
	case errors.Is(err, agentprotocol.ErrPodPortForwardClientLimit),
		errors.Is(err, agentprotocol.ErrPodPortForwardPodLimit):
		return ErrByteLimit
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.PodPortForwardResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	return resultError(response.GetResult(), response.GetReason())
}

func resultError(result agentv1.ResultCode, reason string) error {
	switch result {
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return ErrPodNotFound
	case agentv1.ResultCode_RESULT_CODE_CONFLICT:
		if reason == "PodUIDMismatch" {
			return ErrPodReplaced
		}
		return ErrUpstreamFailure
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return ErrRequestCapacity
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return ErrClusterUnavailable
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	default:
		return ErrUpstreamFailure
	}
}
