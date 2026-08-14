package podexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ErrRecordingNotFound      = errors.New("Pod terminal recording not found")
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
	SessionTTL         time.Duration
	MaxPending         int
	MaxInputBytes      uint64
	MaxOutputBytes     uint64
	MaxRecordingBytes  uint64
	RecordingRetention time.Duration
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
	RecordOutput   bool
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
	RecordingID   string
}

type Result struct {
	ExitCode           int32
	OutputBytes        uint64
	OutputLimitReached bool
	RecordingID        string
	RecordingSaved     bool
}

type RecordingFrame struct {
	OffsetMilliseconds int64  `json:"offset_ms"`
	Stream             string `json:"stream"`
	Data               []byte `json:"data"`
}

type Recording struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// TenantID, ProjectID and ClusterName are resolved by the database on
	// insert, not supplied here: a recording is created from a cluster-scoped
	// route that never sees them. They are read back so a recording stays
	// answerable for by scope once its Cluster is gone.
	TenantID       string           `json:"tenant_id,omitempty"`
	ProjectID      string           `json:"project_id,omitempty"`
	ClusterID      string           `json:"cluster_id"`
	ClusterName    string           `json:"cluster_name,omitempty"`
	Namespace      string           `json:"namespace"`
	PodName        string           `json:"pod_name"`
	PodUID         string           `json:"pod_uid"`
	Container      string           `json:"container"`
	Columns        uint32           `json:"columns"`
	Rows           uint32           `json:"rows"`
	StartedAt      time.Time        `json:"started_at"`
	EndedAt        time.Time        `json:"ended_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	Result         string           `json:"result"`
	ExitCode       int32            `json:"exit_code"`
	OutputBytes    uint64           `json:"output_bytes"`
	RecordingBytes uint64           `json:"recording_bytes"`
	Truncated      bool             `json:"truncated"`
	Frames         []RecordingFrame `json:"frames,omitempty"`
}

type RecordingScope struct {
	ClusterID string
	Namespace string
	PodName   string
}

type RecordingStore interface {
	SaveRecording(context.Context, Recording) error
	ListRecordings(context.Context, RecordingScope, int) ([]Recording, error)
	GetRecording(context.Context, RecordingScope, string) (Recording, error)
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
	recordings  RecordingStore
}

func NewService(requester Requester, recordings RecordingStore, config Config) *Service {
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
	if config.MaxRecordingBytes == 0 || config.MaxRecordingBytes > config.MaxOutputBytes {
		config.MaxRecordingBytes = config.MaxOutputBytes
	}
	if config.RecordingRetention <= 0 {
		config.RecordingRetention = 7 * 24 * time.Hour
	}
	return &Service{
		requester:   requester,
		config:      config,
		pending:     make(map[string]Session),
		idempotency: make(map[string]idempotencyRecord),
		recordings:  recordings,
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
	recordingID := ""
	if input.RecordOutput {
		recordingID, err = identifier.NewUUID()
		if err != nil {
			return Session{}, fmt.Errorf("generate Pod terminal recording identifier: %w", err)
		}
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
		RecordingID:   recordingID,
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

func (service *Service) ConsumeBound(input ConsumeInput) (Session, error) {
	if service == nil || !validation.IsUUID(input.ID) || !validation.IsUUID(input.UserID) ||
		!validation.IsUUID(input.AuthSessionID) || !validation.IsUUID(input.ClusterID) || input.Now.IsZero() {
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
	if session.UserID != input.UserID || session.AuthSessionID != input.AuthSessionID || session.ClusterID != input.ClusterID {
		return Session{}, ErrSessionBindingMismatch
	}
	return session, nil
}

func (service *Service) Run(
	ctx context.Context,
	session Session,
	peer agentprotocol.PodExecPeer,
) (result Result, runErr error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if peer == nil || ctx == nil || !validation.IsUUID(session.ID) {
		return Result{}, ErrInvalidInput
	}
	startedAt := time.Now().UTC()
	var recorder *recordingPeer
	if session.RecordingID != "" {
		recorder = &recordingPeer{
			PodExecPeer: peer,
			startedAt:   startedAt,
			maxBytes:    service.config.MaxRecordingBytes,
		}
		peer = recorder
		result.RecordingID = session.RecordingID
		defer func() {
			if service.recordings == nil {
				return
			}
			endedAt := time.Now().UTC()
			recording := Recording{
				ID: session.RecordingID, UserID: session.UserID,
				ClusterID: session.ClusterID, Namespace: session.Namespace,
				PodName: session.PodName, PodUID: session.PodUID,
				Container: session.Container, Columns: session.Columns, Rows: session.Rows,
				StartedAt: startedAt, EndedAt: endedAt,
				ExpiresAt: endedAt.Add(service.config.RecordingRetention),
				Result:    recordingResult(runErr), ExitCode: result.ExitCode,
				OutputBytes: result.OutputBytes, RecordingBytes: recorder.bytes,
				Truncated: recorder.truncated, Frames: recorder.frames,
			}
			persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			result.RecordingSaved = service.recordings.SaveRecording(persistContext, recording) == nil
		}()
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
		return result, requestError(ctx, err)
	}
	if err := responseError(response); err != nil {
		return result, err
	}
	if err := exitError(exit); err != nil {
		result.OutputBytes = exit.GetOutputBytes()
		result.OutputLimitReached = exit.GetOutputLimitReached()
		return result, err
	}
	result.ExitCode = exit.GetExitCode()
	result.OutputBytes = exit.GetOutputBytes()
	result.OutputLimitReached = exit.GetOutputLimitReached()
	return result, nil
}

func (service *Service) ListRecordings(
	ctx context.Context,
	scope RecordingScope,
) ([]Recording, error) {
	if service == nil || service.recordings == nil || validateRecordingScope(scope) != nil {
		return nil, ErrInvalidInput
	}
	return service.recordings.ListRecordings(ctx, scope, 50)
}

func (service *Service) GetRecording(
	ctx context.Context,
	scope RecordingScope,
	id string,
) (Recording, error) {
	if service == nil || service.recordings == nil || validateRecordingScope(scope) != nil ||
		!validation.IsUUID(id) {
		return Recording{}, ErrInvalidInput
	}
	return service.recordings.GetRecording(ctx, scope, id)
}

func validateRecordingScope(scope RecordingScope) error {
	if !validation.IsUUID(scope.ClusterID) ||
		len(k8svalidation.IsDNS1123Label(scope.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(scope.PodName)) != 0 {
		return ErrInvalidInput
	}
	return nil
}

type recordingPeer struct {
	agentprotocol.PodExecPeer
	startedAt time.Time
	maxBytes  uint64
	bytes     uint64
	truncated bool
	frames    []RecordingFrame
}

func (peer *recordingPeer) Send(ctx context.Context, frame *agentv1.PodExecFrame) error {
	if output := frame.GetOutput(); output != nil && len(output.GetData()) > 0 {
		remaining := peer.maxBytes - peer.bytes
		data := output.GetData()
		if uint64(len(data)) > remaining {
			data = data[:remaining]
			peer.truncated = true
		}
		if len(data) > 0 {
			peer.frames = append(peer.frames, RecordingFrame{
				OffsetMilliseconds: time.Since(peer.startedAt).Milliseconds(),
				Stream:             strings.ToLower(strings.TrimPrefix(output.GetStream().String(), "POD_EXEC_OUTPUT_STREAM_")),
				Data:               append([]byte(nil), data...),
			})
			peer.bytes += uint64(len(data))
		}
	}
	return peer.PodExecPeer.Send(ctx, frame)
}

func recordingResult(err error) string {
	switch {
	case err == nil:
		return "succeeded"
	case errors.Is(err, ErrOutputLimit):
		return "output_limit"
	case errors.Is(err, ErrClusterTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled), errors.Is(err, io.EOF):
		return "canceled"
	default:
		return "failed"
	}
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
		RecordOutput  bool
	}{
		input.AuthSessionID,
		input.ClusterID,
		input.Namespace,
		input.PodName,
		input.PodUID,
		input.Container,
		input.Columns,
		input.Rows,
		input.RecordOutput,
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
