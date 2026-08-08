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
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const maxKubernetesNetworkingMutationRequestBytes = 512 * 1024

type kubernetesNetworkingService interface {
	ListNetworkingResources(context.Context, kubernetesresource.ListNetworkingResourcesInput) (kubernetesresource.NetworkingResourcePage, error)
	GetNetworkingResource(context.Context, string, string, kubernetesresource.NetworkingResource, string) (kubernetesresource.NetworkingResourceDetail, error)
	CreateNetworkingResource(context.Context, kubernetesresource.CreateNetworkingResourceInput) (kubernetesresource.NetworkingResourceDetail, error)
	UpdateNetworkingResource(context.Context, kubernetesresource.UpdateNetworkingResourceInput) (kubernetesresource.NetworkingResourceDetail, error)
	DeleteNetworkingResource(context.Context, kubernetesresource.DeleteNetworkingResourceInput) error
}

type kubernetesNetworkingHandler struct {
	baseHandler
	service kubernetesNetworkingService
}

type networkingConfigurationRequest struct {
	Service      *kubernetesresource.ServiceSpec      `json:"service"`
	Ingress      *kubernetesresource.IngressSpec      `json:"ingress"`
	Gateway      *kubernetesresource.GatewaySpec      `json:"gateway"`
	GatewayRoute *kubernetesresource.GatewayRouteSpec `json:"gateway_route"`
}

type createNetworkingResourceRequest struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	networkingConfigurationRequest
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type updateNetworkingResourceRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	networkingConfigurationRequest
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type deleteNetworkingResourceRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesNetworkingHandler(
	logger *slog.Logger,
	service kubernetesNetworkingService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesNetworkingHandler {
	return &kubernetesNetworkingHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesNetworkingHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resourceName, ok := handler.parseResource(c)
	if !ok {
		return
	}
	input, err := parseNetworkingListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "networking resource query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.Resource = resourceName
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListNetworkingResources(ctx, input)
	cancel()
	if handler.respondNetworkingError(c, "list Kubernetes networking resources", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesNetworkingHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resourceName, ok := handler.parseResource(c)
	if !ok {
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "networking resource detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "networking resource query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetNetworkingResource(
		ctx, c.Param("cluster_id"), c.Param("namespace_name"), resourceName, c.Param("network_name"),
	)
	cancel()
	if handler.respondNetworkingError(c, "get Kubernetes networking resource", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesNetworkingHandler) create(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createNetworkingResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesNetworkingMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource create request")
		return
	}
	identity, _ := kubernetesresource.NetworkingResourceIdentity(resourceName)
	target = resourceTargetName(identity, c.Param("namespace_name"), request.Name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "networking resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateNetworkingResource(ctx, kubernetesresource.CreateNetworkingResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: request.Name, Labels: request.Labels, Annotations: request.Annotations,
		Service: request.Service, Ingress: request.Ingress, Gateway: request.Gateway,
		GatewayRoute: request.GatewayRoute,
		DryRun:       request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondNetworkingError(c, "create Kubernetes networking resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesNetworkingHandler) update(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("network_name"))
	if !ok {
		return
	}
	var request updateNetworkingResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesNetworkingMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource update request")
		return
	}
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceUpdate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "networking resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateNetworkingResource(ctx, kubernetesresource.UpdateNetworkingResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: c.Param("network_name"), UID: request.UID, ResourceVersion: request.ResourceVersion,
		Service: request.Service, Ingress: request.Ingress, Gateway: request.Gateway,
		GatewayRoute: request.GatewayRoute,
		DryRun:       request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondNetworkingError(c, "update Kubernetes networking resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesNetworkingHandler) delete(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("network_name"))
	if !ok {
		return
	}
	var request deleteNetworkingResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesNetworkingMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource delete request")
		return
	}
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceDelete, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "networking resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteNetworkingResource(ctx, kubernetesresource.DeleteNetworkingResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: c.Param("network_name"), UID: request.UID, ResourceVersion: request.ResourceVersion,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondNetworkingError(c, "delete Kubernetes networking resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesNetworkingHandler) parseResource(c *gin.Context) (kubernetesresource.NetworkingResource, bool) {
	resourceName, ok := kubernetesresource.ParseNetworkingResource(c.Param("network_resource"))
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource")
	}
	return resourceName, ok
}

func (handler *kubernetesNetworkingHandler) mutationTarget(
	c *gin.Context,
	action string,
	name string,
) (kubernetesresource.NetworkingResource, string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	resourceName, ok := kubernetesresource.ParseNetworkingResource(c.Param("network_resource"))
	if !ok {
		target := c.Param("network_resource") + " " + c.Param("namespace_name") + "/" + name
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid networking resource")
		return "", "", actor.User.ID, false
	}
	identity, _ := kubernetesresource.NetworkingResourceIdentity(resourceName)
	target := resourceTargetName(identity, c.Param("namespace_name"), name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "networking resource mutation does not accept query parameters")
		return "", "", actor.User.ID, false
	}
	return resourceName, target, actor.User.ID, true
}

func (handler *kubernetesNetworkingHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

func (handler *kubernetesNetworkingHandler) respondNetworkingError(c *gin.Context, operation string, err error) bool {
	if errors.Is(err, kubernetesresource.ErrGatewayAPIUnavailable) {
		writeError(c, http.StatusConflict, "gateway_api_unavailable", "the requested Gateway API resource and version are not installed in the Cluster")
		return true
	}
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseNetworkingListQuery(query url.Values) (kubernetesresource.ListNetworkingResourcesInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListNetworkingResourcesInput{}, err
	}
	result := kubernetesresource.ListNetworkingResourcesInput{
		Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListNetworkingResourcesInput{}, errors.New("invalid networking resource list limit")
		}
		result.Limit = limit
	}
	return result, nil
}
