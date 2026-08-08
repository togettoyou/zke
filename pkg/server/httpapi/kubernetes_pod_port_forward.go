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
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
	"github.com/togettoyou/zke/pkg/server/podportforward"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	podPortForwardWebSocketProtocol = "zke.pod-port-forward.v1"
	maxPodPortForwardCreateBytes    = 2048
	maxPodPortForwardMessageBytes   = 48 * 1024
)

type PodPortForwardHTTPConfig struct {
	MaximumDuration, IdleTimeout, RevalidateInterval, WriteTimeout time.Duration
}

type podPortForwardService interface {
	Create(podportforward.CreateInput) (podportforward.Session, error)
	Consume(podportforward.ConsumeInput) (podportforward.Session, error)
	Run(context.Context, podportforward.Session, agentprotocol.PodPortForwardPeer) (podportforward.Result, error)
}

type kubernetesPodPortForwardHandler struct {
	baseHandler
	service     podPortForwardService
	authService *auth.Service
	rbacService *rbac.Service
	config      PodPortForwardHTTPConfig
	upgrader    websocket.Upgrader
}

type podPortForwardCreateRequest struct {
	PodUID  string `json:"uid"`
	Port    uint32 `json:"port"`
	Confirm bool   `json:"confirm"`
}

type podPortForwardCreateResponse struct {
	SessionID     string    `json:"session_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	WebSocketPath string    `json:"websocket_path"`
	Subprotocol   string    `json:"subprotocol"`
}

type podPortForwardStatus struct {
	Type               string `json:"type"`
	Result             string `json:"result"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	ClientBytes        uint64 `json:"client_bytes,omitempty"`
	PodBytes           uint64 `json:"pod_bytes,omitempty"`
	ClientLimitReached bool   `json:"client_limit_reached,omitempty"`
	PodLimitReached    bool   `json:"pod_limit_reached,omitempty"`
}

func newKubernetesPodPortForwardHandler(logger *slog.Logger, service podPortForwardService,
	authService *auth.Service, rbacService *rbac.Service, auditService *audit.Service,
	operationTimeout time.Duration, config PodPortForwardHTTPConfig) *kubernetesPodPortForwardHandler {
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
	return &kubernetesPodPortForwardHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout), service: service,
		authService: authService, rbacService: rbacService, config: config,
		upgrader: websocket.Upgrader{HandshakeTimeout: config.WriteTimeout, ReadBufferSize: 4096,
			WriteBufferSize: 4096, Subprotocols: []string{podPortForwardWebSocketProtocol}, CheckOrigin: podExecSameOrigin},
	}
}

func (handler *kubernetesPodPortForwardHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	request := podPortForwardCreateRequest{}
	if decodeJSONRequest(c, &request, maxPodPortForwardCreateBytes) != nil {
		handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForwardSessionCreate, podPortForwardTarget(c, "", 0), "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod port-forward session request")
		return
	}
	target := podPortForwardTarget(c, request.PodUID, request.Port)
	if handler.service == nil {
		handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForwardSessionCreate, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod port forwarding is unavailable")
		return
	}
	session, err := handler.service.Create(podportforward.CreateInput{UserID: identity.User.ID,
		AuthSessionID: identity.SessionID, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), PodName: c.Param("pod_name"),
		PodUID: request.PodUID, Port: request.Port, Confirm: request.Confirm, Now: time.Now().UTC()})
	if err != nil {
		handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForwardSessionCreate, target, "failed")
		handler.respondCreateError(c, err)
		return
	}
	handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForwardSessionCreate, target, "succeeded")
	path := fmt.Sprintf("/api/v1/clusters/%s/namespaces/%s/pods/%s/port-forward-sessions/%s",
		session.ClusterID, url.PathEscape(session.Namespace), url.PathEscape(session.PodName), session.ID)
	apiresponse.WriteSuccess(c, http.StatusCreated, podPortForwardCreateResponse{SessionID: session.ID,
		ExpiresAt: session.ExpiresAt, WebSocketPath: path, Subprotocol: podPortForwardWebSocketProtocol})
}

func (handler *kubernetesPodPortForwardHandler) connect(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	if !podExecSameOrigin(c.Request) {
		writeError(c, http.StatusForbidden, "cross_origin_forbidden", "cross-origin request forbidden")
		return
	}
	if !slices.Contains(websocket.Subprotocols(c.Request), podPortForwardWebSocketProtocol) {
		writeError(c, http.StatusBadRequest, "invalid_subprotocol", "Pod port-forward WebSocket subprotocol is required")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod port forwarding is unavailable")
		return
	}
	session, err := handler.service.Consume(podportforward.ConsumeInput{ID: c.Param("session_id"),
		UserID: identity.User.ID, AuthSessionID: identity.SessionID, ClusterID: c.Param("cluster_id"),
		Namespace: c.Param("namespace_name"), PodName: c.Param("pod_name"), Now: time.Now().UTC()})
	if err != nil {
		handler.respondConsumeError(c, err)
		return
	}
	target := podPortForwardTarget(c, session.PodUID, session.Port)
	connection, err := handler.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForward, target, "failed")
		return
	}
	defer connection.Close()
	connection.SetReadLimit(maxPodPortForwardMessageBytes)
	peer := &podPortForwardWebSocketPeer{connection: connection, writeTimeout: handler.config.WriteTimeout}
	peer.touch()
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.config.MaximumDuration)
	revoked := &atomic.Bool{}
	done := handler.monitorAccess(ctx, cancel, c, identity, peer, revoked)
	result, runErr := handler.service.Run(ctx, session, peer)
	cancel()
	<-done
	status := podPortForwardStatus{Type: "exit", Result: "ok", ClientBytes: result.ClientBytes, PodBytes: result.PodBytes,
		ClientLimitReached: result.ClientLimitReached, PodLimitReached: result.PodLimitReached}
	auditResult := "succeeded"
	if runErr != nil {
		status.Result, status.Reason, status.Message = portForwardErrorStatus(runErr, revoked.Load())
		if revoked.Load() {
			auditResult = "denied"
		} else if !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, io.EOF) {
			auditResult = "failed"
		}
	}
	_ = peer.writeStatus(status)
	handler.record(c, identity.User.ID, auditaction.KubernetesPodPortForward, target, auditResult)
}

func (handler *kubernetesPodPortForwardHandler) monitorAccess(ctx context.Context, cancel context.CancelFunc,
	c *gin.Context, identity auth.Identity, peer *podPortForwardWebSocketPeer, revoked *atomic.Bool) <-chan struct{} {
	done := make(chan struct{})
	token, tokenExists := httpmiddleware.SessionToken(c)
	clusterID := c.Param("cluster_id")
	go func() {
		defer close(done)
		defer peer.interruptRead()
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
				if !tokenExists || handler.authService == nil || handler.rbacService == nil {
					revoked.Store(true)
					cancel()
					return
				}
				opCtx, opCancel := context.WithTimeout(rbac.WithoutBindingCache(ctx), handler.operationTimeout)
				current, authErr := handler.authService.Authenticate(opCtx, token, time.Now().UTC())
				if authErr == nil && (current.SessionID != identity.SessionID || current.User.ID != identity.User.ID) {
					authErr = errors.New("session identity changed")
				}
				var authzErr error
				if authErr == nil {
					_, authzErr = handler.rbacService.AuthorizeCluster(opCtx, identity.User.ID, rbac.PermissionClusterPodPortForward, clusterID)
				}
				opCancel()
				if authErr != nil || authzErr != nil {
					revoked.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func (handler *kubernetesPodPortForwardHandler) respondCreateError(c *gin.Context, err error) {
	if errors.Is(err, podportforward.ErrSessionCapacity) {
		c.Header("Retry-After", "1")
	}
	handler.respondError(c, "create Kubernetes Pod port-forward session", err,
		errorMapping{podportforward.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod port-forward session request"},
		errorMapping{podportforward.ErrConfirmationRequired, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required"},
		errorMapping{podportforward.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "idempotency key was already used for another Pod port-forward request"},
		errorMapping{podportforward.ErrSessionCapacity, http.StatusTooManyRequests, "capacity_exhausted", "Pod port-forward session capacity is exhausted"})
}

func (handler *kubernetesPodPortForwardHandler) respondConsumeError(c *gin.Context, err error) {
	handler.respondError(c, "consume Kubernetes Pod port-forward session", err,
		errorMapping{podportforward.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod port-forward session request"},
		errorMapping{podportforward.ErrSessionNotFound, http.StatusNotFound, "session_not_found", "Pod port-forward session was not found or was already used"},
		errorMapping{podportforward.ErrSessionExpired, http.StatusGone, "session_expired", "Pod port-forward session expired"},
		errorMapping{podportforward.ErrSessionBindingMismatch, http.StatusForbidden, "session_binding_mismatch", "Pod port-forward session does not belong to this request"})
}

func (handler *kubernetesPodPortForwardHandler) record(c *gin.Context, userID, action, target, result string) {
	handler.recordOperation(c, auditedOperation{Scope: auditScopeCluster, ActorUserID: userID, Action: action,
		TargetType: auditaction.TargetKubernetesResource, TargetName: target, Result: result})
}

func podPortForwardTarget(c *gin.Context, uid string, port uint32) string {
	return fmt.Sprintf("core/v1/pods %s/%s uid:%s port-forward:%d", c.Param("namespace_name"), c.Param("pod_name"), uid, port)
}

func portForwardErrorStatus(err error, revoked bool) (string, string, string) {
	if revoked {
		return "forbidden", "AccessRevoked", "Pod port-forward access was revoked"
	}
	switch {
	case errors.Is(err, podportforward.ErrAgentNotConnected):
		return "unavailable", "AgentNotConnected", "Cluster Agent is not connected"
	case errors.Is(err, podportforward.ErrAgentUnsupported):
		return "unavailable", "AgentUnsupported", "Cluster Agent does not support Pod Port Forward"
	case errors.Is(err, podportforward.ErrRequestCapacity):
		return "resource_exhausted", "CapacityExhausted", "Pod port-forward capacity is exhausted"
	case errors.Is(err, podportforward.ErrPodNotFound):
		return "not_found", "PodNotFound", "Kubernetes Pod was not found"
	case errors.Is(err, podportforward.ErrPodReplaced):
		return "conflict", "PodUIDMismatch", "Kubernetes Pod was replaced"
	case errors.Is(err, podportforward.ErrByteLimit):
		return "resource_exhausted", "ByteLimitReached", "Pod port-forward byte limit was reached"
	case errors.Is(err, podportforward.ErrClusterAccessDenied):
		return "forbidden", "Forbidden", "Kubernetes API denied Pod Port Forward"
	case errors.Is(err, podportforward.ErrClusterTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout", "Timeout", "Pod port-forward session timed out"
	case errors.Is(err, context.Canceled):
		return "canceled", "Canceled", "Pod port-forward session was canceled"
	default:
		return "internal", "ForwardingFailed", "Pod port-forward session failed"
	}
}

type podPortForwardWebSocketPeer struct {
	connection   *websocket.Conn
	writeTimeout time.Duration
	writeMutex   sync.Mutex
	activityNano atomic.Int64
}

func (peer *podPortForwardWebSocketPeer) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	messageType, data, err := peer.connection.ReadMessage()
	if err != nil {
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return nil, io.EOF
		}
		return nil, err
	}
	if messageType != websocket.BinaryMessage || len(data) == 0 {
		return nil, agentprotocol.ErrStreamProtocol
	}
	peer.touch()
	return data, nil
}

func (peer *podPortForwardWebSocketPeer) Write(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return agentprotocol.ErrStreamProtocol
	}
	return peer.write(ctx, websocket.BinaryMessage, data)
}

func (peer *podPortForwardWebSocketPeer) writeStatus(status podPortForwardStatus) error {
	peer.writeMutex.Lock()
	defer peer.writeMutex.Unlock()
	if err := peer.connection.SetWriteDeadline(time.Now().Add(peer.writeTimeout)); err != nil {
		return err
	}
	if err := peer.connection.WriteJSON(status); err != nil {
		return err
	}
	peer.touch()
	return nil
}

func (peer *podPortForwardWebSocketPeer) write(ctx context.Context, messageType int, data []byte) error {
	peer.writeMutex.Lock()
	defer peer.writeMutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := peer.connection.SetWriteDeadline(time.Now().Add(peer.writeTimeout)); err != nil {
		return err
	}
	if err := peer.connection.WriteMessage(messageType, data); err != nil {
		return err
	}
	peer.touch()
	return nil
}

func (peer *podPortForwardWebSocketPeer) touch() { peer.activityNano.Store(time.Now().UnixNano()) }
func (peer *podPortForwardWebSocketPeer) lastActivity() time.Time {
	return time.Unix(0, peer.activityNano.Load())
}
func (peer *podPortForwardWebSocketPeer) interruptRead() {
	_ = peer.connection.SetReadDeadline(time.Now())
}
