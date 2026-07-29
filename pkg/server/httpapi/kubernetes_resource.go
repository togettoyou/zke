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
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

type genericKubernetesResourceService interface {
	DiscoverResources(
		context.Context,
		string,
	) (kubernetescatalog.Catalog, error)
	ListResources(
		context.Context,
		kubernetesresource.ListResourcesInput,
	) (kubernetesresource.ResourcePage, error)
	GetResource(
		context.Context,
		kubernetesresource.GetResourceInput,
	) (map[string]any, error)
}

type kubernetesResourceHandler struct {
	baseHandler
	service genericKubernetesResourceService
}

func newKubernetesResourceHandler(
	logger *slog.Logger,
	service genericKubernetesResourceService,
	operationTimeout time.Duration,
) *kubernetesResourceHandler {
	return &kubernetesResourceHandler{
		baseHandler: newBaseHandler(logger, nil, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesResourceHandler) discover(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"resource discovery does not accept query parameters",
		)
		return
	}
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"unavailable",
			"Kubernetes resource discovery is unavailable",
		)
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DiscoverResources(
		ctx,
		c.Param("cluster_id"),
	)
	cancel()
	if handler.respondResourceError(c, "discover Kubernetes resources", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesResourceHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseGenericResourceListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"invalid Kubernetes resource query",
		)
		return
	}
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"unavailable",
			"Kubernetes resource query is unavailable",
		)
		return
	}
	input.ClusterID = c.Param("cluster_id")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListResources(ctx, input)
	cancel()
	if handler.respondResourceError(c, "list Kubernetes resources", err) {
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"api_version":          result.APIVersion,
		"kind":                 result.Kind,
		"items":                result.Items,
		"continue_token":       result.ContinueToken,
		"resource_version":     result.ResourceVersion,
		"remaining_item_count": result.RemainingItemCount,
	})
}

func (handler *kubernetesResourceHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	if err != nil {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"invalid Kubernetes resource query",
		)
		return
	}
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"unavailable",
			"Kubernetes resource query is unavailable",
		)
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: c.Param("cluster_id"),
			Resource:  resource,
			Namespace: namespace,
			Name:      c.Param("resource_name"),
		},
	)
	cancel()
	if handler.respondResourceError(c, "get Kubernetes resource", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func parseGenericResourceListQuery(
	query url.Values,
) (kubernetesresource.ListResourcesInput, error) {
	allowed := map[string]struct{}{
		"group": {}, "version": {}, "resource": {}, "namespace": {},
		"limit": {}, "continue": {}, "label_selector": {},
		"field_selector": {}, "resource_version": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListResourcesInput{}, err
	}
	resource, namespace, err := genericResourceIdentity(query)
	if err != nil {
		return kubernetesresource.ListResourcesInput{}, err
	}
	result := kubernetesresource.ListResourcesInput{
		Resource:        resource,
		Namespace:       namespace,
		Limit:           kubernetesresource.DefaultResourceListLimit,
		ContinueToken:   query.Get("continue"),
		LabelSelector:   query.Get("label_selector"),
		FieldSelector:   query.Get("field_selector"),
		ResourceVersion: query.Get("resource_version"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListResourcesInput{}, err
		}
		result.Limit = limit
	}
	return result, nil
}

func parseGenericResourceIdentityQuery(
	query url.Values,
) (kubernetesresource.ResourceIdentity, string, error) {
	allowed := map[string]struct{}{
		"group": {}, "version": {}, "resource": {}, "namespace": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ResourceIdentity{}, "", err
	}
	return genericResourceIdentity(query)
}

func genericResourceIdentity(
	query url.Values,
) (kubernetesresource.ResourceIdentity, string, error) {
	resource := kubernetesresource.ResourceIdentity{
		Group:    query.Get("group"),
		Version:  query.Get("version"),
		Resource: query.Get("resource"),
	}
	if resource.Version == "" || resource.Resource == "" {
		return kubernetesresource.ResourceIdentity{}, "", errors.New(
			"version and resource are required",
		)
	}
	return resource, query.Get("namespace"), nil
}

func validateQueryNames(
	query url.Values,
	allowed map[string]struct{},
) error {
	for name, values := range query {
		if _, exists := allowed[name]; !exists || len(values) != 1 {
			return errors.New("unsupported or duplicate query parameter")
		}
	}
	return nil
}

func (handler *kubernetesResourceHandler) respondResourceError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrRequestCapacity) {
		c.Header("Retry-After", "1")
	}
	return handler.respondError(
		c,
		operation,
		err,
		errorMapping{kubernetesresource.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource request"},
		errorMapping{kubernetesresource.ErrResourceNotFound, http.StatusNotFound, "resource_not_found", "Kubernetes resource not found"},
		errorMapping{kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		errorMapping{kubernetesresource.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support generic resource queries"},
		errorMapping{kubernetesresource.ErrRequestCapacity, http.StatusTooManyRequests, "resource_capacity_exhausted", "resource query capacity is exhausted"},
		errorMapping{kubernetesresource.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		errorMapping{kubernetesresource.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes API request timed out"},
		errorMapping{kubernetesresource.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		errorMapping{kubernetesresource.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read the Kubernetes resource"},
		errorMapping{kubernetesresource.ErrResponseTooLarge, http.StatusBadGateway, "agent_response_too_large", "Agent response exceeded the configured limit"},
		errorMapping{kubernetesresource.ErrUpstreamConflict, http.StatusConflict, "cluster_api_conflict", "Kubernetes resource changed during the request"},
		errorMapping{kubernetesresource.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid resource response"},
		errorMapping{kubernetesresource.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes resource query failed"},
	)
}
