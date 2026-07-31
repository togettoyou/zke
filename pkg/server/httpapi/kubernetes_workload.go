package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type kubernetesWorkloadService interface {
	ListWorkloads(
		context.Context,
		kubernetesresource.ListWorkloadsInput,
	) (kubernetesresource.WorkloadPage, error)
	GetWorkload(
		context.Context,
		string,
		string,
		kubernetesresource.WorkloadResource,
		string,
	) (kubernetesresource.WorkloadDetail, error)
}

type kubernetesWorkloadHandler struct {
	baseHandler
	service kubernetesWorkloadService
}

func newKubernetesWorkloadHandler(
	logger *slog.Logger,
	service kubernetesWorkloadService,
	operationTimeout time.Duration,
) *kubernetesWorkloadHandler {
	return &kubernetesWorkloadHandler{
		baseHandler: newBaseHandler(logger, nil, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesWorkloadHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return
	}
	input, err := parseWorkloadListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.Resource = resource
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListWorkloads(ctx, input)
	cancel()
	if handler.respondWorkloadError(c, "list Kubernetes workloads", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesWorkloadHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "workload detail does not accept query parameters")
		return
	}
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetWorkload(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		resource,
		c.Param("workload_name"),
	)
	cancel()
	if handler.respondWorkloadError(c, "get Kubernetes workload", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func parseWorkloadListQuery(query url.Values) (kubernetesresource.ListWorkloadsInput, error) {
	allowed := map[string]struct{}{
		"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListWorkloadsInput{}, err
	}
	result := kubernetesresource.ListWorkloadsInput{
		Limit:         kubernetesresource.DefaultResourceListLimit,
		ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"),
		FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListWorkloadsInput{}, errors.New("invalid workload list limit")
		}
		result.Limit = limit
	}
	return result, nil
}

func (handler *kubernetesWorkloadHandler) respondWorkloadError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}
