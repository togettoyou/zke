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

const maxKubernetesStorageMutationRequestBytes = 512 * 1024

type kubernetesStorageService interface {
	ListStorageResources(context.Context, kubernetesresource.ListStorageResourcesInput) (kubernetesresource.StorageResourcePage, error)
	GetStorageResource(context.Context, string, string, kubernetesresource.StorageResource, string) (kubernetesresource.StorageResourceDetail, error)
	CreateStorageResource(context.Context, kubernetesresource.CreateStorageResourceInput) (kubernetesresource.StorageResourceDetail, error)
	UpdateStorageResource(context.Context, kubernetesresource.UpdateStorageResourceInput) (kubernetesresource.StorageResourceDetail, error)
	DeleteStorageResource(context.Context, kubernetesresource.DeleteStorageResourceInput) error
}

type kubernetesStorageHandler struct {
	baseHandler
	service kubernetesStorageService
}

type storageCreateConfigurationRequest struct {
	PersistentVolume      *kubernetesresource.PersistentVolumeCreateSpec      `json:"persistent_volume"`
	PersistentVolumeClaim *kubernetesresource.PersistentVolumeClaimCreateSpec `json:"persistent_volume_claim"`
	StorageClass          *kubernetesresource.StorageClassCreateSpec          `json:"storage_class"`
}

type createStorageResourceRequest struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	storageCreateConfigurationRequest
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type storageUpdateConfigurationRequest struct {
	PersistentVolume      *kubernetesresource.PersistentVolumeUpdateSpec      `json:"persistent_volume"`
	PersistentVolumeClaim *kubernetesresource.PersistentVolumeClaimUpdateSpec `json:"persistent_volume_claim"`
	StorageClass          *kubernetesresource.StorageClassUpdateSpec          `json:"storage_class"`
}

type updateStorageResourceRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	storageUpdateConfigurationRequest
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type deleteStorageResourceRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesStorageHandler(
	logger *slog.Logger,
	service kubernetesStorageService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesStorageHandler {
	return &kubernetesStorageHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesStorageHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resourceName, ok := handler.parseResource(c)
	if !ok {
		return
	}
	input, err := parseStorageListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid storage resource query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "storage resource query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.Resource = resourceName
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListStorageResources(ctx, input)
	cancel()
	if handler.respondStorageError(c, "list Kubernetes storage resources", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesStorageHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resourceName, ok := handler.parseResource(c)
	if !ok {
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "storage resource detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "storage resource query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetStorageResource(
		ctx, c.Param("cluster_id"), c.Param("namespace_name"), resourceName, c.Param("storage_name"),
	)
	cancel()
	if handler.respondStorageError(c, "get Kubernetes storage resource", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesStorageHandler) create(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createStorageResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesStorageMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid storage resource create request")
		return
	}
	identity, _ := kubernetesresource.StorageResourceIdentity(resourceName)
	target = resourceTargetName(identity, c.Param("namespace_name"), request.Name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "storage resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateStorageResource(ctx, kubernetesresource.CreateStorageResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: request.Name, Labels: request.Labels, Annotations: request.Annotations,
		PersistentVolume: request.PersistentVolume, PersistentVolumeClaim: request.PersistentVolumeClaim,
		StorageClass: request.StorageClass, DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondStorageError(c, "create Kubernetes storage resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesStorageHandler) update(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("storage_name"))
	if !ok {
		return
	}
	var request updateStorageResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesStorageMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid storage resource update request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "storage resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateStorageResource(ctx, kubernetesresource.UpdateStorageResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: c.Param("storage_name"), UID: request.UID, ResourceVersion: request.ResourceVersion,
		PersistentVolume: request.PersistentVolume, PersistentVolumeClaim: request.PersistentVolumeClaim,
		StorageClass: request.StorageClass, DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondStorageError(c, "update Kubernetes storage resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesStorageHandler) delete(c *gin.Context) {
	resourceName, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("storage_name"))
	if !ok {
		return
	}
	var request deleteStorageResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesStorageMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid storage resource delete request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "storage resource mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteStorageResource(ctx, kubernetesresource.DeleteStorageResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resourceName,
		Name: c.Param("storage_name"), UID: request.UID, ResourceVersion: request.ResourceVersion,
		DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondStorageError(c, "delete Kubernetes storage resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesStorageHandler) parseResource(c *gin.Context) (kubernetesresource.StorageResource, bool) {
	resourceName, ok := kubernetesresource.ParseStorageResource(c.Param("storage_resource"))
	if ok {
		isNamespacedRoute := c.Param("namespace_name") != ""
		ok = isNamespacedRoute == (resourceName == kubernetesresource.StoragePersistentVolumeClaims)
	}
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid storage resource or scope")
	}
	return resourceName, ok
}

func (handler *kubernetesStorageHandler) mutationTarget(c *gin.Context, action, name string) (kubernetesresource.StorageResource, string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	resourceName, ok := handler.parseResource(c)
	if !ok {
		target := c.Param("storage_resource") + " " + c.Param("namespace_name") + "/" + name
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		return "", "", actor.User.ID, false
	}
	identity, _ := kubernetesresource.StorageResourceIdentity(resourceName)
	target := resourceTargetName(identity, c.Param("namespace_name"), name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "storage resource mutation does not accept query parameters")
		return "", "", actor.User.ID, false
	}
	return resourceName, target, actor.User.ID, true
}

func (handler *kubernetesStorageHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

func (handler *kubernetesStorageHandler) respondStorageError(c *gin.Context, operation string, err error) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseStorageListQuery(query url.Values) (kubernetesresource.ListStorageResourcesInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListStorageResourcesInput{}, err
	}
	result := kubernetesresource.ListStorageResourcesInput{
		Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListStorageResourcesInput{}, errors.New("invalid storage resource list limit")
		}
		result.Limit = limit
	}
	return result, nil
}
