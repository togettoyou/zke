package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxCreateAgentInstallationRequestBytes = 16 * 1024

type agentInstallationHandler struct {
	baseHandler
	service *agentinstall.Service
	limiter *sourceLimiter
}

type createAgentInstallationRequest struct {
	ClusterName string `json:"cluster_name"`
}

type createAgentInstallationResponse struct {
	ID             string    `json:"id"`
	ClusterName    string    `json:"cluster_name"`
	ExpiresAt      time.Time `json:"expires_at"`
	ManifestURL    string    `json:"manifest_url"`
	InstallCommand string    `json:"install_command"`
}

func newAgentInstallationHandler(
	logger *slog.Logger,
	service *agentinstall.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
	manifestRateLimit AgentEnrollmentHTTPConfig,
) *agentInstallationHandler {
	return &agentInstallationHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		limiter: newSourceLimiter(
			manifestRateLimit.RateLimitWindow,
			manifestRateLimit.MaxAttemptsPerSource,
		),
	}
}

func (handler *agentInstallationHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster installation is not configured")
		return
	}
	var request createAgentInstallationRequest
	if err := decodeJSONRequest(
		c,
		&request,
		maxCreateAgentInstallationRequestBytes,
	); err != nil {
		identity, _ := httpmiddleware.Identity(c)
		handler.recordInstallationFailure(c, identity.User.ID, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster installation request")
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	operationContext, cancel := handler.operationContext(c)
	result, err := handler.service.Create(operationContext, agentinstall.CreateInput{
		ProjectID:      c.Param("project_id"),
		ClusterName:    request.ClusterName,
		UserID:         identity.User.ID,
		RequestID:      httpmiddleware.RequestID(c),
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		Now:            time.Now().UTC(),
	})
	cancel()
	switch {
	case errors.Is(err, agentinstall.ErrDisabled):
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster installation is disabled")
	case errors.Is(err, enrollment.ErrInvalidInput):
		handler.recordInstallationFailure(c, identity.User.ID, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster installation request")
	case errors.Is(err, enrollment.ErrDenied):
		handler.recordInstallationFailure(c, identity.User.ID, "denied")
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
	case errors.Is(err, enrollment.ErrIdempotencyConflict):
		writeError(c, http.StatusConflict, "idempotency_conflict", "idempotency key was already used")
	case errors.Is(err, context.DeadlineExceeded):
		handler.recordInstallationFailure(c, identity.User.ID, "failed")
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.recordInstallationFailure(c, identity.User.ID, "failed")
		handler.logger.Error(
			"create Cluster installation",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("project_id", c.Param("project_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		writeSuccess(c, http.StatusCreated, createAgentInstallationResponse{
			ID:             result.ID,
			ClusterName:    result.ClusterName,
			ExpiresAt:      responseTime(result.ExpiresAt),
			ManifestURL:    result.ManifestURL,
			InstallCommand: result.InstallCommand,
		})
	}
}

func (handler *agentInstallationHandler) recordInstallationFailure(
	c *gin.Context,
	userID string,
	result string,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeProject,
		ActorUserID: userID,
		Action:      audit.ActionClusterEnrollmentCreate,
		Result:      result,
	})
}

func (handler *agentInstallationHandler) manifest(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	// The manifest is fetched with an installation token, so it is reachable
	// without a session. It is rate limited per source before any other work,
	// like the sibling enrollment endpoint: the token is not guessable, but an
	// unauthenticated endpoint must not be freely repeatable whatever answer it
	// ends up giving.
	now := time.Now().UTC()
	allowed, retry := handler.limiter.allow(clientAddress(c.Request), now)
	if !allowed {
		retrySeconds := max(1, int(math.Ceil(retry.Seconds())))
		c.Header("Retry-After", strconv.Itoa(retrySeconds))
		writeError(
			c,
			http.StatusTooManyRequests,
			"too_many_requests",
			"too many Agent installation manifest requests",
		)
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent installation is not configured")
		return
	}
	token, ok := bearerToken(c.GetHeader("Authorization"))
	if !ok {
		c.Header("WWW-Authenticate", `Bearer realm="zke-agent-install"`)
		writeError(c, http.StatusUnauthorized, "token_rejected", "installation token rejected")
		return
	}
	operationContext, cancel := handler.operationContext(c)
	manifest, err := handler.service.Manifest(
		operationContext,
		token,
		time.Now().UTC(),
	)
	cancel()
	switch {
	case errors.Is(err, agentinstall.ErrDisabled):
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent installation is disabled")
	case errors.Is(err, agentinstall.ErrTokenRejected):
		c.Header("WWW-Authenticate", `Bearer realm="zke-agent-install"`)
		writeError(c, http.StatusUnauthorized, "token_rejected", "installation token rejected")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.logger.Error(
			"render Agent installation manifest",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", manifest)
	}
}
