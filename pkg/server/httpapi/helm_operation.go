package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/helm"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

// Reading a release change while it is still happening.
//
// These two routes are the other half of the write routes next door: those
// start an operation and answer with its identity, and these are where the
// account of it is read. A Console polls the single-operation route once a
// second or two while a deployment runs, and calls the listing once — when a
// window is opened — to find an operation it started before the page was
// reloaded and would otherwise have no way back to.
//
// Neither is audited, and that is a consequence of who may read them rather
// than an omission. An operation is readable only by the operator who started
// it, so its content — including the rendered manifest, which can carry a
// Secret the chart generated — was already handed to exactly this operator in
// answer to exactly this request. Recording an event per poll would add a
// hundred rows saying nothing and bury the one that says the release was
// changed. The change itself is audited by the write route, once, with its
// outcome.

type helmOperationHandler struct {
	baseHandler
	operations *helm.Operations
}

func newHelmOperationHandler(
	logger *slog.Logger,
	operations *helm.Operations,
) *helmOperationHandler {
	return &helmOperationHandler{
		baseHandler: newBaseHandler(logger, nil, 0),
		operations:  operations,
	}
}

func (handler *helmOperationHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.operations == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable",
			"Helm release management is unavailable")
		return
	}
	identifier := c.Param("operation_id")
	if !helm.IsOperationID(identifier) {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Helm operation ID is not valid")
		return
	}
	actor, _ := httpmiddleware.Identity(c)
	operation, found := handler.operations.Get(identifier, actor.User.ID)
	// An operation that belongs to somebody else, one that has expired and one
	// that never existed are one answer. Telling them apart would say whether
	// an identity is in use, which is the only thing an identity this handler
	// will not otherwise talk about is worth guessing at.
	if !found ||
		operation.ClusterID != c.Param("cluster_id") ||
		operation.Namespace != c.Param("namespace_name") {
		writeError(c, http.StatusNotFound, "helm_operation_not_found",
			"this Helm operation is not available; it may have finished long enough ago to be forgotten")
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{"operation": operation})
}

func (handler *helmOperationHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.operations == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable",
			"Helm release management is unavailable")
		return
	}
	actor, _ := httpmiddleware.Identity(c)
	operations := handler.operations.List(
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		actor.User.ID,
	)
	writeSuccess(c, http.StatusOK, gin.H{"operations": operations})
}
