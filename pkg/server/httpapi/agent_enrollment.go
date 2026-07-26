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
const maxCreateAgentEnrollmentRequestBytes = 16 * 1024

type enrollmentHandler struct {
	logger           *slog.Logger
	service          *enrollment.Service
	auditService     *audit.Service
	operationTimeout time.Duration
}

type createEnrollmentResponse struct {
	ID          string    `json:"id"`
	ClusterID   string    `json:"cluster_id,omitempty"`
	ClusterName string    `json:"cluster_name"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type createEnrollmentRequest struct {
	ClusterName string `json:"cluster_name"`
}

type enrollmentResponse struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	ProjectID       string     `json:"project_id"`
	ClusterID       string     `json:"cluster_id,omitempty"`
	ClusterName     string     `json:"cluster_name"`
	CreatedByUserID string     `json:"created_by_user_id"`
	Status          string     `json:"status"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ConsumedAt      *time.Time `json:"consumed_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
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
	var request createEnrollmentRequest
	if err := decodeJSONRequest(
		c,
		&request,
		maxCreateAgentEnrollmentRequestBytes,
	); err != nil {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentCreate, "failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	operationContext, cancelOperation := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.Create(
		operationContext,
		enrollment.CreateInput{
			ProjectID:      c.Param("project_id"),
			ClusterName:    request.ClusterName,
			UserID:         identity.User.ID,
			RequestID:      httpmiddleware.RequestID(c),
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
			Now:            time.Now().UTC(),
		},
	)
	cancelOperation()
	if errors.Is(err, enrollment.ErrInvalidInput) {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentCreate, "failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	if errors.Is(err, enrollment.ErrDenied) {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentCreate, "denied",
		)
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
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentCreate, "failed",
		)
		handler.logger.Warn(
			"create Cluster enrollment timed out",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", identity.User.ID),
			slog.String("project_id", c.Param("project_id")),
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	}
	if err != nil {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentCreate, "failed",
		)
		handler.logger.Error(
			"create Cluster enrollment",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", identity.User.ID),
			slog.String("project_id", c.Param("project_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	c.JSON(http.StatusCreated, createEnrollmentResponse{
		ID:          result.ID,
		ClusterID:   result.ClusterID,
		ClusterName: result.ClusterName,
		Token:       result.Token,
		ExpiresAt:   responseTime(result.ExpiresAt),
	})
}

func (handler *enrollmentHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationTimeout)
	result, err := handler.service.List(ctx, c.Param("project_id"), time.Now().UTC())
	cancel()
	if handler.handleManagementError(c, "list Cluster enrollments", err) {
		return
	}
	response := make([]enrollmentResponse, 0, len(result))
	for _, item := range result {
		response = append(response, responseEnrollment(item))
	}
	c.JSON(http.StatusOK, gin.H{"cluster_enrollments": response})
}

func (handler *enrollmentHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationTimeout)
	result, err := handler.service.Get(
		ctx, c.Param("project_id"), c.Param("enrollment_id"), time.Now().UTC(),
	)
	cancel()
	if handler.handleManagementError(c, "get Cluster enrollment", err) {
		return
	}
	c.JSON(http.StatusOK, responseEnrollment(result))
}

func (handler *enrollmentHandler) revoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxCreateAgentEnrollmentRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentRevoke, "failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationTimeout)
	result, err := handler.service.Revoke(ctx, enrollment.RevokeInput{
		ProjectID: c.Param("project_id"), EnrollmentID: c.Param("enrollment_id"),
		Confirm: request.Confirm, UserID: identity.User.ID,
		RequestID: httpmiddleware.RequestID(c), Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordProjectFailure(
			c, identity.User.ID, audit.ActionClusterEnrollmentRevoke, "failed",
		)
	}
	if handler.handleManagementError(c, "revoke Cluster enrollment", err) {
		return
	}
	c.JSON(http.StatusOK, responseEnrollment(result))
}

func (handler *enrollmentHandler) reenroll(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxCreateAgentEnrollmentRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordClusterFailure(
			c, identity.User.ID, audit.ActionClusterConnectionReenroll, "failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationTimeout)
	result, err := handler.service.CreateReenrollment(ctx, enrollment.ReenrollInput{
		ClusterID: c.Param("cluster_id"), UserID: identity.User.ID,
		RequestID:      httpmiddleware.RequestID(c),
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName), Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordClusterFailure(
			c, identity.User.ID, audit.ActionClusterConnectionReenroll, "failed",
		)
	}
	if handler.handleManagementError(c, "create Cluster reenrollment", err) {
		return
	}
	c.JSON(http.StatusCreated, createEnrollmentResponse{
		ID: result.ID, ClusterID: result.ClusterID, ClusterName: result.ClusterName,
		Token: result.Token, ExpiresAt: responseTime(result.ExpiresAt),
	})
}

func (handler *enrollmentHandler) handleManagementError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, enrollment.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster enrollment request")
	case errors.Is(err, enrollment.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "Cluster enrollment not found")
	case errors.Is(err, enrollment.ErrStateConflict):
		writeError(c, http.StatusConflict, "resource_state_conflict", "Cluster enrollment state conflicts with the request")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	default:
		handler.logger.Error(
			operation,
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
	return true
}

func responseEnrollment(item enrollment.Enrollment) enrollmentResponse {
	return enrollmentResponse{
		ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID,
		ClusterID: item.ClusterID, ClusterName: item.ClusterName,
		CreatedByUserID: item.CreatedByUserID,
		Status:          item.Status, ExpiresAt: responseTime(item.ExpiresAt),
		ConsumedAt: responseTimePointer(item.ConsumedAt),
		RevokedAt:  responseTimePointer(item.RevokedAt),
		CreatedAt:  responseTime(item.CreatedAt),
	}
}

func (handler *enrollmentHandler) recordProjectFailure(
	c *gin.Context,
	userID string,
	action string,
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
			Action:      action,
			Result:      result,
			RequestID:   httpmiddleware.RequestID(c),
		},
	)
	cancelAudit()
	if err != nil {
		handler.logger.Error(
			"record Cluster enrollment failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("project_id", c.Param("project_id")),
			slog.String("result", result),
			slog.String("error", err.Error()),
		)
	}
}

func (handler *enrollmentHandler) recordClusterFailure(
	c *gin.Context,
	userID string,
	action string,
	result string,
) {
	if handler.auditService == nil {
		return
	}
	auditContext, cancelAudit := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	err := handler.auditService.RecordClusterEvent(
		auditContext,
		audit.ClusterEventInput{
			ActorUserID: userID,
			ClusterID:   c.Param("cluster_id"),
			Action:      action,
			Result:      result,
			RequestID:   httpmiddleware.RequestID(c),
		},
	)
	cancelAudit()
	if err != nil {
		handler.logger.Error(
			"record Cluster reenrollment failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("cluster_id", c.Param("cluster_id")),
			slog.String("result", result),
			slog.String("error", err.Error()),
		)
	}
}
