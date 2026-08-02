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

const maxKubernetesConfigMapMutationRequestBytes = 2 * 1024 * 1024

type kubernetesConfigMapService interface {
	ListConfigMaps(context.Context, kubernetesresource.ListConfigMapsInput) (kubernetesresource.ConfigMapPage, error)
	GetConfigMap(context.Context, string, string, string) (kubernetesresource.ConfigMapDetail, error)
	CreateConfigMap(context.Context, kubernetesresource.CreateConfigMapInput) (kubernetesresource.ConfigMapDetail, error)
	UpdateConfigMap(context.Context, kubernetesresource.UpdateConfigMapInput) (kubernetesresource.ConfigMapDetail, error)
	DeleteConfigMap(context.Context, kubernetesresource.DeleteConfigMapInput) error
}

type kubernetesConfigMapHandler struct {
	baseHandler
	service kubernetesConfigMapService
}

type createConfigMapRequest struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Data        map[string]string `json:"data"`
	BinaryData  map[string]string `json:"binary_data"`
	Immutable   bool              `json:"immutable"`
	DryRun      bool              `json:"dry_run"`
	Confirm     bool              `json:"confirm"`
}

type updateConfigMapRequest struct {
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource_version"`
	Data            map[string]string `json:"data"`
	BinaryData      map[string]string `json:"binary_data"`
	Immutable       *bool             `json:"immutable"`
	DryRun          bool              `json:"dry_run"`
	Confirm         bool              `json:"confirm"`
}

type deleteConfigMapRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesConfigMapHandler(
	logger *slog.Logger,
	service kubernetesConfigMapService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesConfigMapHandler {
	return &kubernetesConfigMapHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesConfigMapHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseConfigMapListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid ConfigMap query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "ConfigMap query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListConfigMaps(ctx, input)
	cancel()
	if handler.respondConfigMapError(c, "list Kubernetes ConfigMaps", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesConfigMapHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "ConfigMap detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "ConfigMap query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetConfigMap(
		ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("config_map_name"),
	)
	cancel()
	if handler.respondConfigMapError(c, "get Kubernetes ConfigMap", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesConfigMapHandler) create(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createConfigMapRequest
	if decodeJSONRequest(c, &request, maxKubernetesConfigMapMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid ConfigMap create request")
		return
	}
	target = handler.target(c, request.Name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "ConfigMap mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateConfigMap(ctx, kubernetesresource.CreateConfigMapInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: request.Name,
		Labels: request.Labels, Annotations: request.Annotations,
		Data: request.Data, BinaryData: request.BinaryData, Immutable: request.Immutable,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondConfigMapError(c, "create Kubernetes ConfigMap", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesConfigMapHandler) update(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("config_map_name"))
	if !ok {
		return
	}
	var request updateConfigMapRequest
	if decodeJSONRequest(c, &request, maxKubernetesConfigMapMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid ConfigMap update request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "ConfigMap mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateConfigMap(ctx, kubernetesresource.UpdateConfigMapInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("config_map_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion,
		Data: request.Data, BinaryData: request.BinaryData, Immutable: request.Immutable,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondConfigMapError(c, "update Kubernetes ConfigMap", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesConfigMapHandler) delete(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("config_map_name"))
	if !ok {
		return
	}
	var request deleteConfigMapRequest
	if decodeJSONRequest(c, &request, maxKubernetesConfigMapMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid ConfigMap delete request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "ConfigMap mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteConfigMap(ctx, kubernetesresource.DeleteConfigMapInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("config_map_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondConfigMapError(c, "delete Kubernetes ConfigMap", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesConfigMapHandler) mutationTarget(
	c *gin.Context,
	action string,
	name string,
) (string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "ConfigMap mutation does not accept query parameters")
		return "", actor.User.ID, false
	}
	return target, actor.User.ID, true
}

func (handler *kubernetesConfigMapHandler) target(c *gin.Context, name string) string {
	return resourceTargetName(kubernetesresource.ConfigMapResourceIdentity(), c.Param("namespace_name"), name)
}

func (handler *kubernetesConfigMapHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

func (handler *kubernetesConfigMapHandler) respondConfigMapError(c *gin.Context, operation string, err error) bool {
	if errors.Is(err, kubernetesresource.ErrConfigMapImmutable) {
		writeError(c, http.StatusConflict, "config_map_immutable", "immutable ConfigMap data cannot be changed")
		return true
	}
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseConfigMapListQuery(query url.Values) (kubernetesresource.ListConfigMapsInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListConfigMapsInput{}, err
	}
	result := kubernetesresource.ListConfigMapsInput{
		Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListConfigMapsInput{}, errors.New("invalid ConfigMap list limit")
		}
		result.Limit = limit
	}
	return result, nil
}
