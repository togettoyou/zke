package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const idempotencyKeyHeaderName = "Idempotency-Key"

type enrollmentHandler struct {
	logger           *slog.Logger
	service          *enrollment.Service
	auditService     *audit.Service
	operationTimeout time.Duration
}

type createEnrollmentResponse struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func newEnrollmentHandler(
	logger *slog.Logger,
	service *enrollment.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *enrollmentHandler {
	return &enrollmentHandler{
		logger:           logger,
		service:          service,
		auditService:     auditService,
		operationTimeout: operationTimeout,
	}
}

func (handler *enrollmentHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	operationContext, cancelOperation := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.Create(
		operationContext,
		enrollment.CreateInput{
			ProjectID:      c.Param("project_id"),
			UserID:         identity.User.ID,
			RequestID:      httpmiddleware.RequestID(c),
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
			Now:            time.Now().UTC(),
		},
	)
	cancelOperation()
	if errors.Is(err, enrollment.ErrInvalidInput) {
		handler.recordFailure(c, identity.User.ID, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	if errors.Is(err, enrollment.ErrDenied) {
		handler.recordFailure(c, identity.User.ID, "denied")
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
		return
	}
	if errors.Is(err, enrollment.ErrIdempotencyConflict) {
		writeError(
			c,
			http.StatusConflict,
			"idempotency_conflict",
			"idempotency key was already used",
		)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		handler.recordFailure(c, identity.User.ID, "failed")
		handler.logger.Warn(
			"create Agent enrollment timed out",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", identity.User.ID),
			slog.String("project_id", c.Param("project_id")),
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	}
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "failed")
		handler.logger.Error(
			"create Agent enrollment",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", identity.User.ID),
			slog.String("project_id", c.Param("project_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	c.JSON(http.StatusCreated, createEnrollmentResponse{
		ID:        result.ID,
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	})
}

func (handler *enrollmentHandler) recordFailure(
	c *gin.Context,
	userID string,
	result string,
) {
	if handler.auditService == nil {
		return
	}
	auditContext, cancelAudit := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	err := handler.auditService.RecordProjectEvent(
		auditContext,
		audit.ProjectEventInput{
			ActorUserID: userID,
			ProjectID:   c.Param("project_id"),
			Action:      audit.ActionAgentEnrollmentCreate,
			Result:      result,
			RequestID:   httpmiddleware.RequestID(c),
		},
	)
	cancelAudit()
	if err != nil {
		handler.logger.Error(
			"record Agent enrollment failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("project_id", c.Param("project_id")),
			slog.String("result", result),
			slog.String("error", err.Error()),
		)
	}
}
