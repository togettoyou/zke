package podexec

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
	ErrInvalidInput           = errors.New("invalid Kubernetes Pod terminal input")
	ErrConfirmationRequired   = errors.New("Kubernetes Pod terminal confirmation is required")
	ErrIdempotencyConflict    = errors.New("Pod terminal idempotency conflict")
	ErrSessionCapacity        = errors.New("Pod terminal pending session capacity is exhausted")
	ErrSessionNotFound        = errors.New("Pod terminal session was not found")
	ErrSessionExpired         = errors.New("Pod terminal session expired")
	ErrSessionBindingMismatch = errors.New("Pod terminal session binding does not match")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support Pod Exec")
	ErrRequestCapacity        = errors.New("Pod Exec request capacity is exhausted")
	ErrPodNotFound            = errors.New("Kubernetes Pod not found")
	ErrPodReplaced            = errors.New("Kubernetes Pod identity changed")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes Pod Exec request timed out")
	ErrOutputLimit            = errors.New("Pod terminal output limit reached")
	ErrUpstreamFailure        = errors.New("Kubernetes Pod Exec request failed")
	ErrInvalidResponse        = errors.New("invalid Agent Pod Exec response")
)

type Requester interface {
	RequestPodExec(
		context.Context,
		string,
		*agentv1.PodExecRequest,
		agentprotocol.PodExecPeer,
	) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error)
}

type Config struct {
	SessionTTL     time.Duration
	MaxPending     int
	MaxInputBytes  uint64
	MaxOutputBytes uint64
}

type CreateInput struct {
	UserID         string
	AuthSessionID  string
	IdempotencyKey string
	ClusterID      string
	Namespace      string
	PodName        string
	PodUID         string
	Container      string
	Columns        uint32
	Rows           uint32
	Confirm        bool
	Now            time.Time
}

type ConsumeInput struct {
	ID            string
	UserID        string
	AuthSessionID string
	ClusterID     string
	Namespace     string
	PodName       string
	Now           time.Time
}

type Session struct {
	ID            string
	UserID        string
	AuthSessionID string
	ClusterID     string
	Namespace     string
	PodName       string
	PodUID        string
	Container     string
	Columns       uint32
	Rows          uint32
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

type Result struct {
	ExitCode           int32
	OutputBytes        uint64
	OutputLimitReached bool
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
	if config.MaxInputBytes == 0 {
		config.MaxInputBytes = agentprotocol.DefaultMaxPodExecInputBytes
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = agentprotocol.DefaultMaxPodExecOutputBytes
	}
	return &Service{
		requester:   requester,
		config:      config,
		pending:     make(map[string]Session),
		idempotency: make(map[string]idempotencyRecord),
	}
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
	if len(service.pending) >= service.config.MaxPending ||
		len(service.idempotency) >= service.config.MaxPending {
		return Session{}, ErrSessionCapacity
	}
	sessionID, err := identifier.NewUUID()
	if err != nil {
		return Session{}, fmt.Errorf("generate Pod terminal session identifier: %w", err)
	}
	session := Session{
		ID:            sessionID,
		UserID:        input.UserID,
		AuthSessionID: input.AuthSessionID,
		ClusterID:     input.ClusterID,
		Namespace:     input.Namespace,
		PodName:       input.PodName,
		PodUID:        input.PodUID,
		Container:     input.Container,
		Columns:       input.Columns,
		Rows:          input.Rows,
		CreatedAt:     input.Now,
		ExpiresAt:     input.Now.Add(service.config.SessionTTL),
	}
	service.pending[session.ID] = session
	service.idempotency[key] = idempotencyRecord{fingerprint: fingerprint, session: session}
	return session, nil
}

func (service *Service) Consume(input ConsumeInput) (Session, error) {
	if service == nil || !validation.IsUUID(input.ID) ||
		!validation.IsUUID(input.UserID) || !validation.IsUUID(input.AuthSessionID) ||
		!validation.IsUUID(input.ClusterID) || input.Now.IsZero() ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 {
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
		session.ClusterID != input.ClusterID || session.Namespace != input.Namespace ||
		session.PodName != input.PodName {
		return Session{}, ErrSessionBindingMismatch
	}
	return session, nil
}

func (service *Service) Run(
	ctx context.Context,
	session Session,
	peer agentprotocol.PodExecPeer,
) (Result, error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if peer == nil || ctx == nil || !validation.IsUUID(session.ID) {
		return Result{}, ErrInvalidInput
	}
	response, exit, err := service.requester.RequestPodExec(
		ctx,
		session.ClusterID,
		&agentv1.PodExecRequest{
			Namespace:      session.Namespace,
			PodName:        session.PodName,
			PodUid:         session.PodUID,
			Container:      session.Container,
			Tty:            true,
			Columns:        session.Columns,
			Rows:           session.Rows,
			MaxInputBytes:  service.config.MaxInputBytes,
			MaxOutputBytes: service.config.MaxOutputBytes,
		},
		peer,
	)
	if err != nil {
		return Result{}, requestError(ctx, err)
	}
	if err := responseError(response); err != nil {
		return Result{}, err
	}
	if err := exitError(exit); err != nil {
		return Result{
			OutputBytes:        exit.GetOutputBytes(),
			OutputLimitReached: exit.GetOutputLimitReached(),
		}, err
	}
	return Result{
		ExitCode:           exit.GetExitCode(),
		OutputBytes:        exit.GetOutputBytes(),
		OutputLimitReached: exit.GetOutputLimitReached(),
	}, nil
}

func validateCreateInput(input CreateInput) error {
	if !validation.IsUUID(input.UserID) || !validation.IsUUID(input.AuthSessionID) ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		!validation.IsUUID(input.ClusterID) || input.Now.IsZero() ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 ||
		len(k8svalidation.IsDNS1123Label(input.Container)) != 0 ||
		input.PodUID == "" || len(input.PodUID) > 256 ||
		strings.TrimSpace(input.PodUID) != input.PodUID ||
		input.Columns == 0 || input.Columns > agentprotocol.MaxPodExecDimension ||
		input.Rows == 0 || input.Rows > agentprotocol.MaxPodExecDimension {
		return ErrInvalidInput
	}
	return nil
}

func createFingerprint(input CreateInput) ([sha256.Size]byte, error) {
	data, err := json.Marshal(struct {
		AuthSessionID string
		ClusterID     string
		Namespace     string
		PodName       string
		PodUID        string
		Container     string
		Columns       uint32
		Rows          uint32
	}{
		input.AuthSessionID,
		input.ClusterID,
		input.Namespace,
		input.PodName,
		input.PodUID,
		input.Container,
		input.Columns,
		input.Rows,
	})
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
	case errors.Is(err, agentconn.ErrPodExecCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrPodExecRequestExhausted):
		return ErrRequestCapacity
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.PodExecResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	return resultError(response.GetResult(), response.GetReason())
}

func exitError(exit *agentv1.PodExecExit) error {
	if exit == nil || exit.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	if exit.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	if exit.GetOutputLimitReached() {
		return ErrOutputLimit
	}
	return resultError(exit.GetResult(), exit.GetReason())
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
	case agentv1.ResultCode_RESULT_CODE_INTERNAL:
		return ErrUpstreamFailure
	default:
		return ErrInvalidResponse
	}
}
