package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type clusterTerminalService interface {
	Create(context.Context, clusterterminal.CreateInput) (podexec.Session, error)
	Finish(context.Context, string) error
	Permissions(string) []string
}

type ClusterTerminalHTTPConfig struct {
	CreationTimeout time.Duration
}

const clusterTerminalResponseWriteGrace = 5 * time.Second

type clusterTerminalHandler struct {
	baseHandler
	service         clusterTerminalService
	podExec         *kubernetesPodExecHandler
	rbac            *rbac.Service
	creationTimeout time.Duration
}

type clusterTerminalCreateRequest struct {
	Columns uint32 `json:"columns"`
	Rows    uint32 `json:"rows"`
	Confirm bool   `json:"confirm"`
}

func newClusterTerminalHandler(logger *slog.Logger, service clusterTerminalService, podExec *kubernetesPodExecHandler,
	rbacService *rbac.Service, auditService *audit.Service, operationTimeout time.Duration,
	config ClusterTerminalHTTPConfig) *clusterTerminalHandler {
	creationTimeout := config.CreationTimeout
	if creationTimeout <= 0 {
		creationTimeout = operationTimeout
	}
	return &clusterTerminalHandler{baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service: service, podExec: podExec, rbac: rbacService, creationTimeout: creationTimeout}
}

func (handler *clusterTerminalHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	// Terminal Pods can need a cold image pull. Override the HTTP Server's
	// short, generic response deadline while preserving a finite write bound;
	// the route and downstream Agent Stream remain bounded by creationTimeout.
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(
		time.Now().Add(handler.creationTimeout + clusterTerminalResponseWriteGrace),
	)
	identity, _ := httpmiddleware.Identity(c)
	request := clusterTerminalCreateRequest{}
	target := "cluster:" + c.Param("cluster_id")
	if decodeJSONRequest(c, &request, maxPodExecCreateBytes) != nil || request.Columns == 0 || request.Rows == 0 {
		handler.record(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster terminal session request")
		return
	}
	if !request.Confirm {
		handler.record(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.record(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster terminal is unavailable")
		return
	}
	permissions, err := handler.clusterPermissions(c.Request.Context(), identity.User.ID, c.Param("cluster_id"))
	if err != nil {
		handler.record(c, identity.User.ID, target, "failed")
		handler.logInternal(c, "resolve Cluster terminal permissions", err)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.creationTimeout)
	session, err := handler.service.Create(ctx, clusterterminal.CreateInput{
		UserID: identity.User.ID, AuthSessionID: identity.SessionID, ClusterID: c.Param("cluster_id"),
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		Permissions:    permissions, Columns: request.Columns, Rows: request.Rows, Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.record(c, identity.User.ID, target, "failed")
		handler.respondError(c, "create Cluster terminal session", err,
			errorMapping{clusterterminal.ErrUnavailable, http.StatusServiceUnavailable, "unavailable", "Cluster terminal is not configured"},
			errorMapping{clusterterminal.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with different terminal session input"},
			errorMapping{clusterterminal.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "target Cluster Agent is not connected"},
			errorMapping{clusterterminal.ErrAgentUnsupported, http.StatusConflict, "agent_capability_missing", "target Cluster Agent does not support Cluster terminal sessions"},
			errorMapping{clusterterminal.ErrClusterAccessDenied, http.StatusForbidden, "cluster_access_denied", "Kubernetes denied the Cluster terminal session"},
			errorMapping{clusterterminal.ErrUpstreamTimeout, http.StatusGatewayTimeout, "timeout", "Cluster terminal Pod did not become ready before the creation timeout"})
		return
	}
	handler.record(c, identity.User.ID, target, "succeeded")
	path := fmt.Sprintf("/api/v1/clusters/%s/terminal-sessions/%s", session.ClusterID, session.ID)
	apiresponse.WriteSuccess(c, http.StatusCreated, podExecCreateResponse{SessionID: session.ID,
		ExpiresAt: session.ExpiresAt, WebSocketPath: path, Subprotocol: podExecWebSocketProtocol})
}

func (handler *clusterTerminalHandler) record(c *gin.Context, userID, target, result string) {
	handler.recordOperation(c, auditedOperation{Scope: auditScopeCluster, ActorUserID: userID,
		Action: auditaction.KubernetesTerminalSessionCreate, TargetType: auditaction.TargetKubernetesResource,
		TargetName: target, Result: result})
}

func (handler *clusterTerminalHandler) connect(c *gin.Context) {
	if handler.podExec == nil || handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster terminal is unavailable")
		return
	}
	required := make([]rbac.Permission, 0)
	for _, permission := range handler.service.Permissions(c.Param("session_id")) {
		required = append(required, rbac.Permission(permission))
	}
	handler.podExec.connectSession(c, true, rbac.PermissionClusterTerminalExec, required, handler.service.Finish)
}

func (handler *clusterTerminalHandler) clusterPermissions(ctx context.Context, userID, clusterID string) ([]string, error) {
	if handler.rbac == nil {
		return nil, errors.New("RBAC service is unavailable")
	}
	permissions := make([]string, 0, len(rbac.Permissions()))
	for _, permission := range rbac.Permissions() {
		if !strings.HasPrefix(string(permission), "cluster.") {
			continue
		}
		_, err := handler.rbac.AuthorizeCluster(ctx, userID, permission, clusterID)
		if err == nil {
			permissions = append(permissions, string(permission))
			continue
		}
		if !errors.Is(err, rbac.ErrDenied) {
			return nil, err
		}
	}
	return permissions, nil
}
