package httpapi

import (
	"context"
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

const maxKubernetesNamespaceRequestBytes = 64 * 1024

var namespaceResourceIdentity = kubernetesresource.ResourceIdentity{
	Version:  "v1",
	Resource: "namespaces",
}

type kubernetesNamespaceService interface {
	ListNamespaces(
		context.Context,
		kubernetesresource.ListNamespacesInput,
	) (kubernetesresource.NamespacePage, error)
	GetNamespace(
		context.Context,
		string,
		string,
	) (kubernetesresource.NamespaceDetail, error)
	CreateNamespace(
		context.Context,
		kubernetesresource.CreateNamespaceInput,
	) (kubernetesresource.NamespaceDetail, error)
	DeleteNamespace(
		context.Context,
		kubernetesresource.DeleteNamespaceInput,
	) error
}

type kubernetesNamespaceHandler struct {
	baseHandler
	service kubernetesNamespaceService
}

type createKubernetesNamespaceRequest struct {
	Name    string            `json:"name"`
	Labels  map[string]string `json:"labels"`
	DryRun  bool              `json:"dry_run"`
	Confirm bool              `json:"confirm"`
}

type deleteKubernetesNamespaceRequest struct {
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

func newKubernetesNamespaceHandler(
	logger *slog.Logger,
	service kubernetesNamespaceService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesNamespaceHandler {
	return &kubernetesNamespaceHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesNamespaceHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseNamespaceListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Namespace query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Namespace query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListNamespaces(ctx, input)
	cancel()
	if handler.respondNamespaceError(c, "list Kubernetes Namespaces", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesNamespaceHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Namespace detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Namespace query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetNamespace(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
	)
	cancel()
	if handler.respondNamespaceError(c, "get Kubernetes Namespace", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesNamespaceHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createKubernetesNamespaceRequest
	if decodeJSONRequest(c, &request, maxKubernetesNamespaceRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourceCreate, "", "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Namespace create request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceCreate,
		request.DryRun,
	)
	target := resourceTargetName(namespaceResourceIdentity, "", request.Name)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Namespace mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateNamespace(
		ctx,
		kubernetesresource.CreateNamespaceInput{
			ClusterID:      c.Param("cluster_id"),
			Name:           request.Name,
			Labels:         request.Labels,
			DryRun:         request.DryRun,
			Confirm:        request.Confirm,
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	if err != nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondNamespaceError(c, "create Kubernetes Namespace", err) {
		return
	}
	handler.recordMutation(c, identity.User.ID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"namespace": result, "dry_run": request.DryRun})
}

func (handler *kubernetesNamespaceHandler) delete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request deleteKubernetesNamespaceRequest
	target := resourceTargetName(
		namespaceResourceIdentity,
		"",
		c.Param("namespace_name"),
	)
	if decodeJSONRequest(c, &request, maxKubernetesNamespaceRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Namespace delete request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceDelete,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Namespace mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteNamespace(ctx, kubernetesresource.DeleteNamespaceInput{
		ClusterID:       c.Param("cluster_id"),
		Name:            c.Param("namespace_name"),
		UID:             request.UID,
		ResourceVersion: request.ResourceVersion,
		DryRun:          request.DryRun,
		Confirm:         request.Confirm,
		IdempotencyKey:  c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondNamespaceError(c, "delete Kubernetes Namespace", err) {
		return
	}
	handler.recordMutation(c, identity.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"deleted": !request.DryRun,
		"dry_run": request.DryRun,
		"target":  target,
	})
}

func parseNamespaceListQuery(
	query url.Values,
) (kubernetesresource.ListNamespacesInput, error) {
	allowed := map[string]struct{}{
		"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListNamespacesInput{}, err
	}
	result := kubernetesresource.ListNamespacesInput{
		Limit:         kubernetesresource.DefaultResourceListLimit,
		ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"),
		FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListNamespacesInput{}, err
		}
		result.Limit = limit
	}
	return result, nil
}

func (handler *kubernetesNamespaceHandler) respondNamespaceError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func (handler *kubernetesNamespaceHandler) recordMutation(
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
