package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	podExecWebSocketProtocol = "zke.pod-exec.v1"
	maxPodExecCreateBytes    = 4096
	maxPodExecMessageBytes   = 48 * 1024
)

type PodExecHTTPConfig struct {
	MaximumDuration    time.Duration
	IdleTimeout        time.Duration
	RevalidateInterval time.Duration
	WriteTimeout       time.Duration
}

type podExecService interface {
	Create(podexec.CreateInput) (podexec.Session, error)
	Consume(podexec.ConsumeInput) (podexec.Session, error)
	ConsumeBound(podexec.ConsumeInput) (podexec.Session, error)
	Run(context.Context, podexec.Session, agentprotocol.PodExecPeer) (podexec.Result, error)
	ListRecordings(context.Context, podexec.RecordingScope) ([]podexec.Recording, error)
	GetRecording(context.Context, podexec.RecordingScope, string) (podexec.Recording, error)
}

type kubernetesPodExecHandler struct {
	baseHandler
	service     podExecService
	authService *auth.Service
	rbacService *rbac.Service
	config      PodExecHTTPConfig
	upgrader    websocket.Upgrader
}

type podExecCreateRequest struct {
	PodUID       string `json:"uid"`
	Container    string `json:"container"`
	Columns      uint32 `json:"columns"`
	Rows         uint32 `json:"rows"`
	Confirm      bool   `json:"confirm"`
	RecordOutput bool   `json:"record_output"`
}

type podExecCreateResponse struct {
	SessionID     string    `json:"session_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	WebSocketPath string    `json:"websocket_path"`
	Subprotocol   string    `json:"subprotocol"`
	RecordingID   string    `json:"recording_id,omitempty"`
}

type podExecWireMessage struct {
	Type               string `json:"type"`
	Data               []byte `json:"data,omitempty"`
	Columns            uint32 `json:"columns,omitempty"`
	Rows               uint32 `json:"rows,omitempty"`
	Result             string `json:"result,omitempty"`
	ExitCode           int32  `json:"exit_code,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	OutputBytes        uint64 `json:"output_bytes,omitempty"`
	OutputLimitReached bool   `json:"output_limit_reached,omitempty"`
	RecordingID        string `json:"recording_id,omitempty"`
	RecordingSaved     *bool  `json:"recording_saved,omitempty"`
}

func newKubernetesPodExecHandler(
	logger *slog.Logger,
	service podExecService,
	authService *auth.Service,
	rbacService *rbac.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
	config PodExecHTTPConfig,
) *kubernetesPodExecHandler {
	if config.MaximumDuration <= 0 {
		config.MaximumDuration = 15 * time.Minute
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 2 * time.Minute
	}
	if config.RevalidateInterval <= 0 {
		config.RevalidateInterval = 15 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 5 * time.Second
	}
	return &kubernetesPodExecHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		authService: authService,
		rbacService: rbacService,
		config:      config,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: config.WriteTimeout,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			Subprotocols:     []string{podExecWebSocketProtocol},
			CheckOrigin:      podExecSameOrigin,
		},
	}
}

func (handler *kubernetesPodExecHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	request := podExecCreateRequest{}
	target := podExecTarget(c, request.PodUID, request.Container)
	if decodeJSONRequest(c, &request, maxPodExecCreateBytes) != nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExecSessionCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod terminal session request")
		return
	}
	target = podExecTarget(c, request.PodUID, request.Container)
	if request.RecordOutput && !handler.authorizeRecordingCreate(c, identity.User.ID, target) {
		return
	}
	if handler.service == nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExecSessionCreate, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod terminal is unavailable")
		return
	}
	session, err := handler.service.Create(podexec.CreateInput{
		UserID:         identity.User.ID,
		AuthSessionID:  identity.SessionID,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		ClusterID:      c.Param("cluster_id"),
		Namespace:      c.Param("namespace_name"),
		PodName:        c.Param("pod_name"),
		PodUID:         request.PodUID,
		Container:      request.Container,
		Columns:        request.Columns,
		Rows:           request.Rows,
		Confirm:        request.Confirm,
		RecordOutput:   request.RecordOutput,
		Now:            time.Now().UTC(),
	})
	if err != nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExecSessionCreate, target, "failed")
		if request.RecordOutput {
			handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingCreate, target, "failed")
		}
		handler.respondCreatePodExecError(c, err)
		return
	}
	handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExecSessionCreate, target, "succeeded")
	path := fmt.Sprintf(
		"/api/v1/clusters/%s/namespaces/%s/pods/%s/terminal-sessions/%s",
		session.ClusterID,
		url.PathEscape(session.Namespace),
		url.PathEscape(session.PodName),
		session.ID,
	)
	apiresponse.WriteSuccess(c, http.StatusCreated, podExecCreateResponse{
		SessionID:     session.ID,
		ExpiresAt:     session.ExpiresAt,
		WebSocketPath: path,
		Subprotocol:   podExecWebSocketProtocol,
		RecordingID:   session.RecordingID,
	})
}

func (handler *kubernetesPodExecHandler) connect(c *gin.Context) {
	handler.connectSession(c, false, rbac.PermissionClusterPodExec, nil, nil)
}

func (handler *kubernetesPodExecHandler) connectSession(
	c *gin.Context,
	bound bool,
	permission rbac.Permission,
	requiredPermissions []rbac.Permission,
	finish func(context.Context, string) error,
) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	if !podExecSameOrigin(c.Request) {
		writeError(c, http.StatusForbidden, "cross_origin_forbidden", "cross-origin request forbidden")
		return
	}
	if !slices.Contains(websocket.Subprotocols(c.Request), podExecWebSocketProtocol) {
		writeError(c, http.StatusBadRequest, "invalid_subprotocol", "Pod terminal WebSocket subprotocol is required")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod terminal is unavailable")
		return
	}
	consume := podexec.ConsumeInput{
		ID:            c.Param("session_id"),
		UserID:        identity.User.ID,
		AuthSessionID: identity.SessionID,
		ClusterID:     c.Param("cluster_id"),
		Now:           time.Now().UTC(),
	}
	var session podexec.Session
	var err error
	if bound {
		session, err = handler.service.ConsumeBound(consume)
	} else {
		consume.Namespace, consume.PodName = c.Param("namespace_name"), c.Param("pod_name")
		session, err = handler.service.Consume(consume)
	}
	if err != nil {
		handler.respondConsumePodExecError(c, err)
		return
	}
	if finish != nil {
		defer func() {
			cleanupContext, cancelCleanup := context.WithTimeout(context.WithoutCancel(c.Request.Context()), handler.operationTimeout)
			defer cancelCleanup()
			if cleanupErr := finish(cleanupContext, session.ID); cleanupErr != nil {
				handler.logInternal(c, "clean up Cluster terminal session", cleanupErr)
			}
		}()
	}
	target := fmt.Sprintf("core/v1/pods %s/%s uid:%s exec:%s", session.Namespace, session.PodName, session.PodUID, session.Container)
	connection, err := handler.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExec, target, "failed")
		return
	}
	defer func() { _ = connection.Close() }()
	connection.SetReadLimit(maxPodExecMessageBytes)
	peer := &podExecWebSocketPeer{
		connection:   connection,
		writeTimeout: handler.config.WriteTimeout,
	}
	peer.touch()
	connection.SetPongHandler(func(string) error {
		peer.touch()
		return nil
	})

	sessionContext, cancelSession := context.WithTimeout(c.Request.Context(), handler.config.MaximumDuration)
	accessRevoked := &atomic.Bool{}
	monitorDone := handler.monitorPodExecAccess(
		sessionContext,
		cancelSession,
		c,
		identity,
		peer,
		accessRevoked,
		session.RecordingID != "",
		permission,
		requiredPermissions,
	)
	runResult, runErr := handler.service.Run(sessionContext, session, peer)
	cancelSession()
	<-monitorDone
	result := "succeeded"
	if runErr != nil {
		result = podExecAuditResult(sessionContext, runErr, accessRevoked.Load())
		if !peer.exitSent.Load() {
			_ = peer.sendServiceExit(runErr, accessRevoked.Load())
		}
		if !errors.Is(runErr, context.Canceled) {
			handler.logInternal(c, "run Kubernetes Pod terminal", runErr)
		}
	}
	if runResult.RecordingID != "" {
		saved := runResult.RecordingSaved
		_ = peer.write(context.Background(), podExecWireMessage{
			Type: "recording", RecordingID: runResult.RecordingID, RecordingSaved: &saved,
		})
		recordingResult := "failed"
		if saved {
			recordingResult = "succeeded"
		}
		handler.recordPodExec(
			c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingCreate,
			target+" recording:"+runResult.RecordingID, recordingResult,
		)
	}
	handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodExec, target, result)
}

func (handler *kubernetesPodExecHandler) authorizeRecordingCreate(
	c *gin.Context,
	userID string,
	target string,
) bool {
	if handler.rbacService == nil {
		handler.recordPodExec(c, userID, auditaction.DeniedClusterPodTerminalRecordingCreate, target, "denied")
		writeError(c, http.StatusForbidden, "forbidden", "Pod terminal recording permission is required")
		return false
	}
	ctx, cancel := handler.operationContext(c)
	_, err := handler.rbacService.AuthorizeCluster(
		ctx, userID, rbac.PermissionClusterPodTerminalRecordingCreate, c.Param("cluster_id"),
	)
	cancel()
	if err == nil {
		return true
	}
	handler.recordPodExec(c, userID, auditaction.DeniedClusterPodTerminalRecordingCreate, target, "denied")
	if errors.Is(err, rbac.ErrDenied) {
		writeError(c, http.StatusForbidden, "forbidden", "Pod terminal recording permission is required")
	} else {
		handler.logInternal(c, "authorize Pod terminal recording", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
	return false
}

func (handler *kubernetesPodExecHandler) listRecordings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	target := podExecTarget(c, "", "recordings")
	if handler.service == nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingList, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod terminal recordings are unavailable")
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "terminal recording list does not accept query parameters")
		return
	}
	ctx, cancel := handler.operationContext(c)
	items, err := handler.service.ListRecordings(ctx, podExecRecordingScope(c))
	cancel()
	if err != nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingList, target, "failed")
		handler.respondRecordingError(c, "list Pod terminal recordings", err)
		return
	}
	handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingList, target, "succeeded")
	writeSuccess(c, http.StatusOK, items)
}

func (handler *kubernetesPodExecHandler) getRecording(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	target := podExecTarget(c, "", "recording:"+c.Param("recording_id"))
	if handler.service == nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingRead, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod terminal recordings are unavailable")
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "terminal recording detail does not accept query parameters")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.GetRecording(ctx, podExecRecordingScope(c), c.Param("recording_id"))
	cancel()
	if err != nil {
		handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingRead, target, "failed")
		handler.respondRecordingError(c, "get Pod terminal recording", err)
		return
	}
	handler.recordPodExec(c, identity.User.ID, auditaction.KubernetesPodTerminalRecordingRead, target, "succeeded")
	writeSuccess(c, http.StatusOK, item)
}

func podExecRecordingScope(c *gin.Context) podexec.RecordingScope {
	return podexec.RecordingScope{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"),
		PodName: c.Param("pod_name"),
	}
}

func (handler *kubernetesPodExecHandler) respondRecordingError(c *gin.Context, operation string, err error) {
	handler.respondError(c, operation, err,
		errorMapping{podexec.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod terminal recording request"},
		errorMapping{podexec.ErrRecordingNotFound, http.StatusNotFound, "recording_not_found", "Pod terminal recording was not found or has expired"},
	)
}

func (handler *kubernetesPodExecHandler) monitorPodExecAccess(
	ctx context.Context,
	cancel context.CancelFunc,
	c *gin.Context,
	identity auth.Identity,
	peer *podExecWebSocketPeer,
	revoked *atomic.Bool,
	recording bool,
	permission rbac.Permission,
	requiredPermissions []rbac.Permission,
) <-chan struct{} {
	done := make(chan struct{})
	sessionToken, tokenExists := httpmiddleware.SessionToken(c)
	clusterID := c.Param("cluster_id")
	go func() {
		defer close(done)
		ticker := time.NewTicker(handler.config.RevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(peer.lastActivity()) > handler.config.IdleTimeout {
					cancel()
					return
				}
				if err := peer.ping(); err != nil {
					cancel()
					return
				}
				if !tokenExists || handler.authService == nil || handler.rbacService == nil {
					revoked.Store(true)
					cancel()
					return
				}
				operationContext, cancelOperation := context.WithTimeout(
					rbac.WithoutBindingCache(ctx),
					handler.operationTimeout,
				)
				current, authErr := handler.authService.Authenticate(
					operationContext,
					sessionToken,
					time.Now().UTC(),
				)
				if authErr == nil &&
					(current.SessionID != identity.SessionID || current.User.ID != identity.User.ID) {
					authErr = errors.New("session identity changed")
				}
				var authorizationErr error
				if authErr == nil {
					_, authorizationErr = handler.rbacService.AuthorizeCluster(
						operationContext,
						identity.User.ID,
						permission,
						clusterID,
					)
					if authorizationErr == nil && recording {
						_, authorizationErr = handler.rbacService.AuthorizeCluster(
							operationContext,
							identity.User.ID,
							rbac.PermissionClusterPodTerminalRecordingCreate,
							clusterID,
						)
					}
					if authorizationErr == nil {
						for _, required := range requiredPermissions {
							if required == permission {
								continue
							}
							_, authorizationErr = handler.rbacService.AuthorizeCluster(
								operationContext, identity.User.ID, required, clusterID,
							)
							if authorizationErr != nil {
								break
							}
						}
					}
				}
				cancelOperation()
				if authErr != nil || authorizationErr != nil {
					revoked.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func (handler *kubernetesPodExecHandler) respondCreatePodExecError(c *gin.Context, err error) {
	if errors.Is(err, podexec.ErrSessionCapacity) {
		c.Header("Retry-After", "1")
	}
	handler.respondError(
		c,
		"create Kubernetes Pod terminal session",
		err,
		errorMapping{podexec.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod terminal session request"},
		errorMapping{podexec.ErrConfirmationRequired, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required"},
		errorMapping{podexec.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "idempotency key was already used for another Pod terminal request"},
		errorMapping{podexec.ErrSessionCapacity, http.StatusTooManyRequests, "capacity_exhausted", "Pod terminal session capacity is exhausted"},
	)
}

func (handler *kubernetesPodExecHandler) respondConsumePodExecError(c *gin.Context, err error) {
	handler.respondError(
		c,
		"consume Kubernetes Pod terminal session",
		err,
		errorMapping{podexec.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod terminal session request"},
		errorMapping{podexec.ErrSessionNotFound, http.StatusNotFound, "session_not_found", "Pod terminal session was not found or was already used"},
		errorMapping{podexec.ErrSessionExpired, http.StatusGone, "session_expired", "Pod terminal session expired"},
		errorMapping{podexec.ErrSessionBindingMismatch, http.StatusForbidden, "session_binding_mismatch", "Pod terminal session does not belong to this request"},
	)
}

func (handler *kubernetesPodExecHandler) recordPodExec(
	c *gin.Context,
	actorUserID string,
	action string,
	targetName string,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  targetName,
		Result:      result,
	})
}

func podExecTarget(c *gin.Context, podUID string, container string) string {
	return fmt.Sprintf(
		"core/v1/pods %s/%s uid:%s exec:%s",
		c.Param("namespace_name"),
		c.Param("pod_name"),
		podUID,
		container,
	)
}

func podExecSameOrigin(request *http.Request) bool {
	if request == nil {
		return false
	}
	origin := request.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	expectedScheme := "http"
	if request.TLS != nil {
		expectedScheme = "https"
	}
	return parsed.Scheme == expectedScheme && strings.EqualFold(parsed.Host, request.Host)
}

// podExecAuditResult says how a terminal session ended, in the audit's
// vocabulary — `succeeded`, `failed` or `denied`, the only three results an
// audit event may carry.
//
// It said `timeout` and `canceled` before, and the audit service drops an event
// whose result is none of the three: a terminal the operator simply closed, or
// one the Server ended at its maximum duration, left no audit record at all —
// the sessions that ended normally were the ones missing from the log.
//
// Closing a terminal is how a terminal ends, so it is a success; so is the
// Server's own duration cap. An upstream timeout is not, and revocation is a
// denial. See kubernetesEventAuditResult for the same rule on Event streams.
func podExecAuditResult(ctx context.Context, err error, revoked bool) string {
	if revoked {
		return "denied"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "succeeded"
	}
	if errors.Is(err, podexec.ErrClusterTimeout) {
		return "failed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return "succeeded"
	}
	return "failed"
}

type podExecWebSocketPeer struct {
	connection   *websocket.Conn
	writeTimeout time.Duration
	writeMutex   sync.Mutex
	activityNano atomic.Int64
	exitSent     atomic.Bool
}

func (peer *podExecWebSocketPeer) Receive(ctx context.Context) (*agentv1.PodExecFrame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	message := podExecWireMessage{}
	if err := peer.connection.ReadJSON(&message); err != nil {
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return nil, io.EOF
		}
		return nil, err
	}
	peer.touch()
	switch message.Type {
	case "stdin":
		if len(message.Data) == 0 {
			return nil, agentprotocol.ErrStreamProtocol
		}
		return &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Input{
			Input: &agentv1.PodExecInput{Data: message.Data},
		}}, nil
	case "resize":
		return &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Resize{
			Resize: &agentv1.PodExecResize{Columns: message.Columns, Rows: message.Rows},
		}}, nil
	case "close_stdin":
		return &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_CloseInput{
			CloseInput: &agentv1.PodExecCloseInput{},
		}}, nil
	default:
		return nil, agentprotocol.ErrStreamProtocol
	}
}

func (peer *podExecWebSocketPeer) Send(
	ctx context.Context,
	frame *agentv1.PodExecFrame,
) error {
	message := podExecWireMessage{}
	switch value := frame.GetMessage().(type) {
	case *agentv1.PodExecFrame_Output:
		message.Type = strings.ToLower(strings.TrimPrefix(
			value.Output.GetStream().String(),
			"POD_EXEC_OUTPUT_STREAM_",
		))
		message.Data = value.Output.GetData()
	case *agentv1.PodExecFrame_Exit:
		message = podExecExitMessage(value.Exit)
		peer.exitSent.Store(true)
	default:
		return agentprotocol.ErrStreamProtocol
	}
	return peer.write(ctx, message)
}

func (peer *podExecWebSocketPeer) sendServiceExit(err error, revoked bool) error {
	reason := "TerminalFailed"
	message := "Pod terminal session failed"
	result := "internal"
	switch {
	case revoked:
		reason, message, result = "AccessRevoked", "Pod terminal access was revoked", "forbidden"
	case errors.Is(err, podexec.ErrAgentNotConnected):
		reason, message, result = "AgentNotConnected", "Cluster Agent is not connected", "unavailable"
	case errors.Is(err, podexec.ErrAgentUnsupported):
		reason, message, result = "AgentUnsupported", "Cluster Agent does not support Pod Exec", "unavailable"
	case errors.Is(err, podexec.ErrRequestCapacity):
		reason, message, result = "CapacityExhausted", "Pod terminal capacity is exhausted", "resource_exhausted"
	case errors.Is(err, podexec.ErrPodNotFound):
		reason, message, result = "PodNotFound", "Kubernetes Pod was not found", "not_found"
	case errors.Is(err, podexec.ErrPodReplaced):
		reason, message, result = "PodUIDMismatch", "Kubernetes Pod was replaced", "conflict"
	case errors.Is(err, podexec.ErrClusterAccessDenied):
		reason, message, result = "Forbidden", "Kubernetes API denied Pod Exec", "forbidden"
	case errors.Is(err, podexec.ErrClusterTimeout), errors.Is(err, context.DeadlineExceeded):
		reason, message, result = "Timeout", "Pod terminal session timed out", "timeout"
	case errors.Is(err, context.Canceled):
		reason, message, result = "Canceled", "Pod terminal session was canceled", "canceled"
	}
	peer.exitSent.Store(true)
	return peer.write(context.Background(), podExecWireMessage{
		Type: "exit", Result: result, Reason: reason, Message: message,
	})
}

func podExecExitMessage(exit *agentv1.PodExecExit) podExecWireMessage {
	return podExecWireMessage{
		Type:               "exit",
		Result:             strings.ToLower(strings.TrimPrefix(exit.GetResult().String(), "RESULT_CODE_")),
		ExitCode:           exit.GetExitCode(),
		Reason:             exit.GetReason(),
		Message:            exit.GetMessage(),
		OutputBytes:        exit.GetOutputBytes(),
		OutputLimitReached: exit.GetOutputLimitReached(),
	}
}

func (peer *podExecWebSocketPeer) write(ctx context.Context, message podExecWireMessage) error {
	peer.writeMutex.Lock()
	defer peer.writeMutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := peer.connection.SetWriteDeadline(time.Now().Add(peer.writeTimeout)); err != nil {
		return err
	}
	if err := peer.connection.WriteJSON(message); err != nil {
		return err
	}
	peer.touch()
	return nil
}

func (peer *podExecWebSocketPeer) ping() error {
	peer.writeMutex.Lock()
	defer peer.writeMutex.Unlock()
	return peer.connection.WriteControl(
		websocket.PingMessage,
		nil,
		time.Now().Add(peer.writeTimeout),
	)
}

func (peer *podExecWebSocketPeer) touch() {
	peer.activityNano.Store(time.Now().UnixNano())
}

func (peer *podExecWebSocketPeer) lastActivity() time.Time {
	return time.Unix(0, peer.activityNano.Load())
}
