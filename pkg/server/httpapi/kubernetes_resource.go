package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

const maxKubernetesMutationRequestBytes = 4 * 1024 * 1024

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
	CreateResource(
		context.Context,
		kubernetesresource.CreateResourceInput,
	) (map[string]any, error)
	UpdateResource(
		context.Context,
		kubernetesresource.UpdateResourceInput,
	) (map[string]any, error)
	PatchResource(
		context.Context,
		kubernetesresource.PatchResourceInput,
	) (map[string]any, error)
	DeleteResource(
		context.Context,
		kubernetesresource.DeleteResourceInput,
	) error
}

type kubernetesResourceHandler struct {
	baseHandler
	service genericKubernetesResourceService
}

func newKubernetesResourceHandler(
	logger *slog.Logger,
	service genericKubernetesResourceService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesResourceHandler {
	return &kubernetesResourceHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

type kubernetesMutationOptionsRequest struct {
	DryRun       bool   `json:"dry_run"`
	FieldManager string `json:"field_manager"`
	Force        bool   `json:"force"`
}

type createKubernetesResourceRequest struct {
	Object  map[string]any                   `json:"object"`
	Options kubernetesMutationOptionsRequest `json:"options"`
	Confirm bool                             `json:"confirm"`
}

type updateKubernetesResourceRequest struct {
	Object  map[string]any                   `json:"object"`
	Options kubernetesMutationOptionsRequest `json:"options"`
	Confirm bool                             `json:"confirm"`
}

type patchKubernetesResourceRequest struct {
	PatchType string                           `json:"patch_type"`
	Patch     json.RawMessage                  `json:"patch"`
	Options   kubernetesMutationOptionsRequest `json:"options"`
	Confirm   bool                             `json:"confirm"`
}

type deleteKubernetesResourcePreconditionsRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

type deleteKubernetesResourceRequest struct {
	DryRun             bool                                         `json:"dry_run"`
	Confirm            bool                                         `json:"confirm"`
	GracePeriodSeconds *int64                                       `json:"grace_period_seconds"`
	PropagationPolicy  string                                       `json:"propagation_policy"`
	Preconditions      deleteKubernetesResourcePreconditionsRequest `json:"preconditions"`
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
	filtered := result.Resources[:0]
	for _, resource := range result.Resources {
		if !kubernetesresource.IsAuthorizationResourceIdentity(kubernetesresource.ResourceIdentity{
			Group: resource.Group, Version: resource.Version, Resource: resource.Resource,
		}) {
			filtered = append(filtered, resource)
		}
	}
	result.Resources = filtered
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

func (handler *kubernetesResourceHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	identity, _ := httpmiddleware.Identity(c)
	var request createKubernetesResourceRequest
	if err != nil ||
		decodeJSONRequest(
			c,
			&request,
			maxKubernetesMutationRequestBytes,
		) != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			auditaction.KubernetesResourceCreate,
			resourceTargetName(resource, namespace, ""),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource create request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceCreate,
		request.Options.DryRun,
	)
	if !request.Options.DryRun && !request.Confirm {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, objectName(request.Object)),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, objectName(request.Object)),
			"failed",
		)
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID:      c.Param("cluster_id"),
			Resource:       resource,
			Namespace:      namespace,
			Object:         request.Object,
			Options:        mutationOptions(request.Options),
			Confirm:        request.Confirm,
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	if err != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, objectName(request.Object)),
			"failed",
		)
	}
	if handler.respondResourceError(c, "create Kubernetes resource", err) {
		return
	}
	handler.recordKubernetesMutation(
		c,
		identity.User.ID,
		action,
		resourceTargetName(resource, namespace, objectName(result)),
		"succeeded",
	)
	status := http.StatusCreated
	if request.Options.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{
		"resource": result,
		"dry_run":  request.Options.DryRun,
	})
}

func (handler *kubernetesResourceHandler) update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	identity, _ := httpmiddleware.Identity(c)
	var request updateKubernetesResourceRequest
	if err != nil ||
		decodeJSONRequest(
			c,
			&request,
			maxKubernetesMutationRequestBytes,
		) != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			auditaction.KubernetesResourceUpdate,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource update request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		request.Options.DryRun,
	)
	if !request.Options.DryRun && !request.Confirm {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID:      c.Param("cluster_id"),
			Resource:       resource,
			Namespace:      namespace,
			Name:           c.Param("resource_name"),
			Object:         request.Object,
			Options:        mutationOptions(request.Options),
			Confirm:        request.Confirm,
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	handler.finishObjectMutation(
		c,
		identity.User.ID,
		action,
		resourceTargetName(resource, namespace, c.Param("resource_name")),
		request.Options.DryRun,
		result,
		err,
		"update Kubernetes resource",
	)
}

func (handler *kubernetesResourceHandler) patch(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	identity, _ := httpmiddleware.Identity(c)
	var request patchKubernetesResourceRequest
	if err != nil ||
		decodeJSONRequest(
			c,
			&request,
			maxKubernetesMutationRequestBytes,
		) != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			auditaction.KubernetesResourcePatch,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource patch request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourcePatch,
		request.Options.DryRun,
	)
	if !request.Options.DryRun && !request.Confirm {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes resource mutation is unavailable")
		return
	}
	patchType, ok := protocolPatchType(request.PatchType)
	if !ok {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes patch type")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID:      c.Param("cluster_id"),
			Resource:       resource,
			Namespace:      namespace,
			Name:           c.Param("resource_name"),
			PatchType:      patchType,
			Patch:          request.Patch,
			Options:        mutationOptions(request.Options),
			Confirm:        request.Confirm,
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	handler.finishObjectMutation(
		c,
		identity.User.ID,
		action,
		resourceTargetName(resource, namespace, c.Param("resource_name")),
		request.Options.DryRun,
		result,
		err,
		"patch Kubernetes resource",
	)
}

func (handler *kubernetesResourceHandler) delete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	identity, _ := httpmiddleware.Identity(c)
	var request deleteKubernetesResourceRequest
	if err != nil ||
		decodeJSONRequest(
			c,
			&request,
			maxKubernetesMutationRequestBytes,
		) != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			auditaction.KubernetesResourceDelete,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource delete request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceDelete,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes resource mutation is unavailable")
		return
	}
	propagation, ok := protocolDeletePropagation(request.PropagationPolicy)
	if !ok {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			resourceTargetName(resource, namespace, c.Param("resource_name")),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes deletion propagation policy")
		return
	}
	targetName := resourceTargetName(
		resource,
		namespace,
		c.Param("resource_name"),
	)
	ctx, cancel := handler.operationContext(c)
	err = handler.service.DeleteResource(
		ctx,
		kubernetesresource.DeleteResourceInput{
			ClusterID:          c.Param("cluster_id"),
			Resource:           resource,
			Namespace:          namespace,
			Name:               c.Param("resource_name"),
			DryRun:             request.DryRun,
			Confirm:            request.Confirm,
			GracePeriodSeconds: request.GracePeriodSeconds,
			Propagation:        propagation,
			Preconditions: kubernetesresource.DeletePreconditions{
				UID:             request.Preconditions.UID,
				ResourceVersion: request.Preconditions.ResourceVersion,
			},
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	if err != nil {
		handler.recordKubernetesMutation(
			c,
			identity.User.ID,
			action,
			targetName,
			"failed",
		)
	}
	if handler.respondResourceError(c, "delete Kubernetes resource", err) {
		return
	}
	handler.recordKubernetesMutation(
		c,
		identity.User.ID,
		action,
		targetName,
		"succeeded",
	)
	writeSuccess(c, http.StatusOK, gin.H{
		"deleted": !request.DryRun,
		"dry_run": request.DryRun,
		"target":  targetName,
	})
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
	if kubernetesresource.IsAuthorizationResourceIdentity(resource) {
		return kubernetesresource.ResourceIdentity{}, "", errors.New(
			"Kubernetes authorization resources require the dedicated API",
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
	if errors.Is(err, kubernetesresource.ErrRequestCapacity) ||
		errors.Is(err, kubernetesresource.ErrResponseBudget) {
		c.Header("Retry-After", "1")
	}
	return handler.respondError(c, operation, err, kubernetesResourceErrorMappings()...)
}

func kubernetesResourceErrorMappings() []errorMapping {
	return []errorMapping{
		{kubernetesresource.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Kubernetes resource request"},
		{kubernetesresource.ErrResourceNotFound, http.StatusNotFound, "resource_not_found", "Kubernetes resource not found"},
		{kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		{kubernetesresource.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support generic resource queries"},
		{kubernetesresource.ErrRequestCapacity, http.StatusTooManyRequests, "resource_capacity_exhausted", "resource query capacity is exhausted"},
		{kubernetesresource.ErrResponseBudget, http.StatusTooManyRequests, "response_budget_exhausted", "Server response buffer budget is exhausted"},
		{kubernetesresource.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		{kubernetesresource.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes API request timed out"},
		{kubernetesresource.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		{kubernetesresource.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read the Kubernetes resource"},
		{kubernetesresource.ErrResponseTooLarge, http.StatusBadGateway, "agent_response_too_large", "Agent response exceeded the configured limit"},
		{kubernetesresource.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "idempotency key was already used for another Kubernetes resource request"},
		{kubernetesresource.ErrUpstreamConflict, http.StatusConflict, "cluster_api_conflict", "Kubernetes resource changed during the request"},
		{kubernetesresource.ErrManagedResource, http.StatusConflict, "managed_resource", "ZKE-managed authorization resources cannot be changed through this API"},
		{kubernetesresource.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid resource response"},
		{kubernetesresource.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes resource query failed"},
	}
}

func mutationOptions(
	request kubernetesMutationOptionsRequest,
) kubernetesresource.MutationOptions {
	return kubernetesresource.MutationOptions{
		DryRun:       request.DryRun,
		FieldManager: request.FieldManager,
		Force:        request.Force,
	}
}

func protocolPatchType(value string) (agentv1.PatchType, bool) {
	switch value {
	case "json":
		return agentv1.PatchType_PATCH_TYPE_JSON, true
	case "merge":
		return agentv1.PatchType_PATCH_TYPE_MERGE, true
	case "strategic-merge":
		return agentv1.PatchType_PATCH_TYPE_STRATEGIC_MERGE, true
	case "apply":
		return agentv1.PatchType_PATCH_TYPE_APPLY, true
	default:
		return agentv1.PatchType_PATCH_TYPE_UNSPECIFIED, false
	}
}

func protocolDeletePropagation(
	value string,
) (agentv1.DeletePropagation, bool) {
	switch value {
	case "":
		return agentv1.DeletePropagation_DELETE_PROPAGATION_UNSPECIFIED, true
	case "orphan":
		return agentv1.DeletePropagation_DELETE_PROPAGATION_ORPHAN, true
	case "background":
		return agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND, true
	case "foreground":
		return agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND, true
	default:
		return agentv1.DeletePropagation_DELETE_PROPAGATION_UNSPECIFIED, false
	}
}

func objectName(object map[string]any) string {
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}

func resourceTargetName(
	resource kubernetesresource.ResourceIdentity,
	namespace string,
	name string,
) string {
	group := resource.Group
	if group == "" {
		group = "core"
	}
	target := group + "/" + resource.Version + "/" + resource.Resource
	if namespace != "" {
		target += " " + namespace
	}
	if name != "" {
		target += "/" + name
	}
	return strings.TrimSpace(target)
}

func (handler *kubernetesResourceHandler) finishObjectMutation(
	c *gin.Context,
	actorUserID string,
	action string,
	targetName string,
	dryRun bool,
	result map[string]any,
	err error,
	operation string,
) {
	if err != nil {
		handler.recordKubernetesMutation(
			c,
			actorUserID,
			action,
			targetName,
			"failed",
		)
	}
	if handler.respondResourceError(c, operation, err) {
		return
	}
	handler.recordKubernetesMutation(
		c,
		actorUserID,
		action,
		targetName,
		"succeeded",
	)
	writeSuccess(c, http.StatusOK, gin.H{
		"resource": result,
		"dry_run":  dryRun,
	})
}

func (handler baseHandler) recordKubernetesMutation(
	c *gin.Context,
	actorUserID string,
	action string,
	targetName string,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  targetName,
		Result:      result,
	})
}

func kubernetesMutationAuditAction(action string, dryRun bool) string {
	if !dryRun {
		return action
	}
	switch action {
	case auditaction.KubernetesResourceCreate:
		return auditaction.KubernetesResourceCreateDryRun
	case auditaction.KubernetesResourceUpdate:
		return auditaction.KubernetesResourceUpdateDryRun
	case auditaction.KubernetesResourcePatch:
		return auditaction.KubernetesResourcePatchDryRun
	case auditaction.KubernetesResourceDelete:
		return auditaction.KubernetesResourceDeleteDryRun
	default:
		return action
	}
}
