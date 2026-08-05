package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/shared/auditctx"
)

// baseHandler holds what every HTTP handler needs: a logger, the audit service
// used to record refused operations, and the per-operation timeout. Handlers
// embed it so the timeout and error-mapping behaviour cannot drift apart
// between resources.
type baseHandler struct {
	logger           *slog.Logger
	auditService     *audit.Service
	operationTimeout time.Duration
}

func newBaseHandler(
	logger *slog.Logger,
	auditService *audit.Service,
	operationTimeout time.Duration,
) baseHandler {
	return baseHandler{
		logger:           logger,
		auditService:     auditService,
		operationTimeout: operationTimeout,
	}
}

// operationContext bounds a single downstream operation. It derives from the
// request context so a disconnected client also cancels the database work.
func (handler baseHandler) operationContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), handler.operationTimeout)
}

// auditContext bounds an audit write. Unlike operationContext it survives the
// client hanging up, because a refused operation has to be recorded whether or
// not anyone is still listening for the response.
func (handler baseHandler) auditContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return auditctx.Detach(c.Request.Context(), handler.operationTimeout)
}

// errorMapping translates one domain sentinel into an HTTP response.
type errorMapping struct {
	target  error
	status  int
	code    string
	message string
}

// detailedError is a failure that already says something the caller can act on,
// and whose account is returned in place of the mapping's fixed message.
//
// Being this interface is the opt-in: only a failure whose detail describes what
// the caller sent — never the Server's or the cluster's internals — implements
// it, so no mapping has to remember to allow it.
type detailedError interface {
	error
	Detail() string
}

// respondError writes the response for err and reports whether it handled one.
// Transport-level outcomes are mapped identically everywhere; a resource only
// supplies the mappings for its own sentinels. Anything unmapped becomes a 500
// and is logged with the request scope, never returned to the client.
func (handler baseHandler) respondError(
	c *gin.Context,
	operation string,
	err error,
	mappings ...errorMapping,
) bool {
	if err == nil {
		return false
	}
	for _, mapping := range mappings {
		if errors.Is(err, mapping.target) {
			message := mapping.message
			var detailed detailedError
			if errors.As(err, &detailed) && detailed.Detail() != "" {
				message = detailed.Detail()
			}
			writeError(c, mapping.status, mapping.code, message)
			return true
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// A timeout is a symptom worth investigating even though the client
		// already learns about it from the status code.
		handler.logger.Warn(
			operation+" timed out",
			append(
				[]any{slog.String("request_id", httpmiddleware.RequestID(c))},
				httpmiddleware.ScopeAttributes(c)...,
			)...,
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case errors.Is(err, context.Canceled):
		// The client is gone; nothing will read the body, but the status keeps
		// the access log honest about why the request ended.
		writeError(c, http.StatusRequestTimeout, "client_closed", "client closed the request")
	default:
		handler.logInternal(c, operation, err)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
	return true
}

func (handler baseHandler) logInternal(
	c *gin.Context,
	operation string,
	err error,
) {
	attributes := []any{
		slog.String("request_id", httpmiddleware.RequestID(c)),
		slog.String("error", err.Error()),
	}
	handler.logger.Error(
		operation,
		append(attributes, httpmiddleware.ScopeAttributes(c)...)...,
	)
}

// auditScope names the scope an audit record belongs to. It mirrors the scope
// the route authorized, so a refused operation is recorded where an operator
// reviewing that Tenant, Project or Cluster will actually find it.
type auditScope string

const (
	auditScopeGlobal  auditScope = "global"
	auditScopeTenant  auditScope = "tenant"
	auditScopeProject auditScope = "project"
	auditScopeCluster auditScope = "cluster"
)

// auditedOperation describes an operation result that must be written to the
// audit trail. Kubernetes mutations use it for success as well as failure;
// database-backed mutations normally record success in their own transaction.
type auditedOperation struct {
	Scope       auditScope
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
}

type failedOperation = auditedOperation

// recordFailure preserves the failure-oriented call sites used by the
// database-backed handlers.
func (handler baseHandler) recordFailure(
	c *gin.Context,
	operation failedOperation,
) {
	handler.recordOperation(c, operation)
}

// recordOperation writes an operation result to the audit trail. Audit
// writes never block the response: a failure to record is logged, because
// losing the response is worse than losing one audit row, but it must not pass
// silently either.
func (handler baseHandler) recordOperation(
	c *gin.Context,
	operation auditedOperation,
) {
	if handler.auditService == nil {
		return
	}
	result := operation.Result
	if result == "" {
		result = "failed"
	}
	ctx, cancel := handler.auditContext(c)
	defer cancel()

	requestID := httpmiddleware.RequestID(c)
	targetID := operation.TargetID
	if targetID == "" {
		targetID = c.Param(operation.TargetType + "_id")
	}
	var err error
	switch operation.Scope {
	case auditScopeTenant:
		err = handler.auditService.RecordTenantEvent(ctx, audit.TenantEventInput{
			ActorUserID: operation.ActorUserID,
			TenantID:    c.Param("tenant_id"),
			Action:      operation.Action,
			TargetType:  operation.TargetType,
			TargetID:    targetID,
			TargetName:  operation.TargetName,
			Result:      result,
			RequestID:   requestID,
		})
	case auditScopeProject:
		err = handler.auditService.RecordProjectEvent(ctx, audit.ProjectEventInput{
			ActorUserID: operation.ActorUserID,
			ProjectID:   c.Param("project_id"),
			Action:      operation.Action,
			TargetType:  operation.TargetType,
			TargetID:    targetID,
			TargetName:  operation.TargetName,
			Result:      result,
			RequestID:   requestID,
		})
	case auditScopeCluster:
		err = handler.auditService.RecordClusterEvent(ctx, audit.ClusterEventInput{
			ActorUserID: operation.ActorUserID,
			ClusterID:   c.Param("cluster_id"),
			Action:      operation.Action,
			TargetType:  operation.TargetType,
			TargetID:    targetID,
			TargetName:  operation.TargetName,
			Result:      result,
			RequestID:   requestID,
		})
	default:
		err = handler.auditService.RecordGlobalEvent(ctx, audit.GlobalEventInput{
			ActorUserID: operation.ActorUserID,
			Action:      operation.Action,
			TargetType:  operation.TargetType,
			TargetID:    targetID,
			TargetName:  operation.TargetName,
			Result:      result,
			RequestID:   requestID,
		})
	}
	if err == nil {
		return
	}
	attributes := []any{
		slog.String("request_id", requestID),
		slog.String("action", operation.Action),
		slog.String("error", err.Error()),
	}
	handler.logger.Error(
		"record operation audit",
		append(attributes, httpmiddleware.ScopeAttributes(c)...)...,
	)
}
