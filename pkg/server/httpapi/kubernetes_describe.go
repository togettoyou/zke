package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
)

type kubernetesDescribeService interface {
	DescribePod(
		context.Context,
		kubernetesdescribe.PodInput,
	) (kubernetesdescribe.Result, error)
	DescribeResource(
		context.Context,
		kubernetesdescribe.ResourceInput,
	) (kubernetesdescribe.Result, error)
}

type kubernetesDescribeHandler struct {
	baseHandler
	service kubernetesDescribeService
}

func newKubernetesDescribeHandler(
	logger *slog.Logger,
	service kubernetesDescribeService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesDescribeHandler {
	return &kubernetesDescribeHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesDescribeHandler) pod(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Pod describe does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable",
			"Kubernetes describe is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DescribePod(
		ctx,
		kubernetesdescribe.PodInput{
			ClusterID: c.Param("cluster_id"),
			Namespace: c.Param("namespace_name"),
			Name:      c.Param("pod_name"),
		},
	)
	cancel()
	handler.finish(c, "describe Kubernetes Pod", result, err)
}

func (handler *kubernetesDescribeHandler) resource(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"invalid Kubernetes resource query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable",
			"Kubernetes describe is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DescribeResource(
		ctx,
		kubernetesdescribe.ResourceInput{
			ClusterID: c.Param("cluster_id"),
			Resource:  resource,
			Namespace: namespace,
			Name:      c.Param("resource_name"),
		},
	)
	cancel()
	handler.finish(c, "describe Kubernetes resource", result, err)
}

func (handler *kubernetesDescribeHandler) finish(
	c *gin.Context,
	operation string,
	result kubernetesdescribe.Result,
	err error,
) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	if resourceHandler.respondResourceError(c, operation, err) {
		return
	}
	handler.recordEventRead(c, result)
	writeSuccess(c, http.StatusOK, result)
}

// recordEventRead keeps the Event audit trail complete across both ways of
// reading Events.
//
// A describe reads the same Events the Event stream does, under the same
// permission, so it leaves the same record — otherwise the trail would say an
// identity never read a Namespace's Events while the Console was showing them
// on every Pod they opened. It records only when Events were actually asked
// for: a Cluster-scoped object carries none, and there is nothing to record
// about a read that never happened.
func (handler *kubernetesDescribeHandler) recordEventRead(
	c *gin.Context,
	result kubernetesdescribe.Result,
) {
	if result.Events.Omitted == kubernetesdescribe.EventsOmittedUnsupportedScope {
		return
	}
	outcome := "succeeded"
	if result.Events.Omitted != "" {
		outcome = "failed"
	}
	identity, _ := httpmiddleware.Identity(c)
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: identity.User.ID,
		Action:      auditaction.KubernetesEventRead,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName: fmt.Sprintf(
			"core/v1/events namespace:%s resource_uid:%s",
			result.Target.Namespace,
			result.Target.UID,
		),
		Result: outcome,
	})
}
