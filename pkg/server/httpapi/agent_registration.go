package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxAgentEnrollmentRequestBytes = 128 * 1024

type AgentEnrollmentHTTPConfig struct {
	OperationTimeout     time.Duration
	RateLimitWindow      time.Duration
	MaxAttemptsPerSource int
}

type agentRegistrationHandler struct {
	logger           *slog.Logger
	service          *enrollment.Service
	operationTimeout time.Duration
	limiter          *sourceLimiter
}

type agentRegistrationRequest struct {
	CSRPEM          string `json:"csr_pem"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion string `json:"protocol_version"`
}

type agentRegistrationResponse struct {
	ClusterID            string    `json:"cluster_id"`
	AgentID              string    `json:"agent_id"`
	CertificatePEM       string    `json:"certificate_pem"`
	CertificateExpiresAt time.Time `json:"certificate_expires_at"`
}

func newAgentRegistrationHandler(
	logger *slog.Logger,
	service *enrollment.Service,
	config AgentEnrollmentHTTPConfig,
) *agentRegistrationHandler {
	return &agentRegistrationHandler{
		logger:           logger,
		service:          service,
		operationTimeout: config.OperationTimeout,
		limiter: newSourceLimiter(
			config.RateLimitWindow,
			config.MaxAttemptsPerSource,
		),
	}
}

func (handler *agentRegistrationHandler) enroll(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	now := time.Now().UTC()
	allowed, retry := handler.limiter.allow(clientAddress(c.Request), now)
	if !allowed {
		retrySeconds := max(1, int(math.Ceil(retry.Seconds())))
		c.Header("Retry-After", strconv.Itoa(retrySeconds))
		writeError(
			c,
			http.StatusTooManyRequests,
			"too_many_requests",
			"too many Agent enrollment attempts",
		)
		return
	}

	token, ok := bearerToken(c.GetHeader("Authorization"))
	c.Request.Header.Del("Authorization")
	if !ok {
		c.Header(
			"WWW-Authenticate",
			`Bearer realm="zke-agent-enrollment", error="invalid_token"`,
		)
		writeError(
			c,
			http.StatusUnauthorized,
			"invalid_enrollment_token",
			"invalid enrollment token",
		)
		return
	}
	var request agentRegistrationRequest
	if err := decodeJSONRequest(c, &request, maxAgentEnrollmentRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"service_unavailable",
			"Agent enrollment is not configured",
		)
		return
	}

	operationContext, cancelOperation := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.Enroll(operationContext, enrollment.EnrollInput{
		Token:           token,
		IdempotencyKey:  c.GetHeader(idempotencyKeyHeaderName),
		CSRPEM:          []byte(request.CSRPEM),
		AgentVersion:    request.AgentVersion,
		ProtocolVersion: request.ProtocolVersion,
		RequestID:       httpmiddleware.RequestID(c),
		Now:             now,
	})
	cancelOperation()
	token = ""
	request.CSRPEM = ""
	if errors.Is(err, enrollment.ErrInvalidInput) {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	if errors.Is(err, enrollment.ErrTokenRejected) {
		c.Header(
			"WWW-Authenticate",
			`Bearer realm="zke-agent-enrollment", error="invalid_token"`,
		)
		writeError(
			c,
			http.StatusUnauthorized,
			"invalid_enrollment_token",
			"invalid enrollment token",
		)
		return
	}
	if errors.Is(err, enrollment.ErrAttemptConflict) {
		writeError(
			c,
			http.StatusConflict,
			"idempotency_conflict",
			"idempotency key was already used with different enrollment data",
		)
		return
	}
	if errors.Is(err, enrollment.ErrAttemptFailed) {
		writeError(
			c,
			http.StatusConflict,
			"enrollment_failed",
			"enrollment attempt cannot be resumed",
		)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		handler.logger.Warn(
			"enroll Agent timed out",
			slog.String("request_id", httpmiddleware.RequestID(c)),
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	}
	if errors.Is(err, enrollment.ErrSigningUnavailable) {
		if err != enrollment.ErrSigningUnavailable {
			handler.logger.Error(
				"record unavailable Agent enrollment audit",
				slog.String("request_id", httpmiddleware.RequestID(c)),
				slog.String("error", err.Error()),
			)
		}
		writeError(
			c,
			http.StatusServiceUnavailable,
			"service_unavailable",
			"Agent enrollment is not configured",
		)
		return
	}
	if err != nil {
		handler.logger.Error(
			"enroll Agent",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeSuccess(c, status, agentRegistrationResponse{
		ClusterID:            result.ClusterID,
		AgentID:              result.AgentID,
		CertificatePEM:       result.CertificatePEM,
		CertificateExpiresAt: responseTime(result.CertificateExpiresAt),
	})
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 ||
		!strings.EqualFold(parts[0], "Bearer") ||
		parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
