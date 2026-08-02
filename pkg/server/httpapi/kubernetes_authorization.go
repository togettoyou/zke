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

const maxKubernetesAuthorizationMutationRequestBytes = 1024 * 1024

type kubernetesAuthorizationService interface {
	ListAuthorizationResources(context.Context, kubernetesresource.ListAuthorizationResourcesInput) (kubernetesresource.AuthorizationResourcePage, error)
	GetAuthorizationResource(context.Context, string, string, kubernetesresource.AuthorizationResource, string) (kubernetesresource.AuthorizationResourceDetail, error)
	CreateAuthorizationResource(context.Context, kubernetesresource.CreateAuthorizationResourceInput) (kubernetesresource.AuthorizationResourceDetail, error)
	UpdateAuthorizationResource(context.Context, kubernetesresource.UpdateAuthorizationResourceInput) (kubernetesresource.AuthorizationResourceDetail, error)
	DeleteAuthorizationResource(context.Context, kubernetesresource.DeleteAuthorizationResourceInput) error
}

type kubernetesAuthorizationHandler struct {
	baseHandler
	service kubernetesAuthorizationService
}

type createAuthorizationResourceRequest struct {
	Name                         string                                       `json:"name"`
	Labels                       map[string]string                            `json:"labels"`
	Annotations                  map[string]string                            `json:"annotations"`
	AutomountServiceAccountToken *bool                                        `json:"automount_service_account_token"`
	Rules                        []kubernetesresource.AuthorizationPolicyRule `json:"rules"`
	Subjects                     []kubernetesresource.AuthorizationSubject    `json:"subjects"`
	RoleRef                      *kubernetesresource.AuthorizationRoleRef     `json:"role_ref"`
	DryRun                       bool                                         `json:"dry_run"`
	Confirm                      bool                                         `json:"confirm"`
}

type updateAuthorizationResourceRequest struct {
	UID                          string                                       `json:"uid"`
	ResourceVersion              string                                       `json:"resource_version"`
	AutomountServiceAccountToken *bool                                        `json:"automount_service_account_token"`
	Rules                        []kubernetesresource.AuthorizationPolicyRule `json:"rules"`
	Subjects                     []kubernetesresource.AuthorizationSubject    `json:"subjects"`
	DryRun                       bool                                         `json:"dry_run"`
	Confirm                      bool                                         `json:"confirm"`
}

type deleteAuthorizationResourceRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesAuthorizationHandler(logger *slog.Logger, service kubernetesAuthorizationService, auditService *audit.Service, operationTimeout time.Duration) *kubernetesAuthorizationHandler {
	return &kubernetesAuthorizationHandler{baseHandler: newBaseHandler(logger, auditService, operationTimeout), service: service}
}

func (handler *kubernetesAuthorizationHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseAuthorizationResourceListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization query")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.Resource = kubernetesresource.AuthorizationResource(c.Param("authorization_resource"))
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes authorization query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListAuthorizationResources(ctx, input)
	cancel()
	if handler.respondAuthorizationError(c, "list Kubernetes authorization resources", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesAuthorizationHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "authorization detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes authorization query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetAuthorizationResource(ctx, c.Param("cluster_id"), c.Param("namespace_name"), kubernetesresource.AuthorizationResource(c.Param("authorization_resource")), c.Param("authorization_name"))
	cancel()
	if handler.respondAuthorizationError(c, "get Kubernetes authorization resource", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesAuthorizationHandler) create(c *gin.Context) {
	resource, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createAuthorizationResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesAuthorizationMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization create request")
		return
	}
	target = handler.target(c, resource, request.Name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes authorization mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateAuthorizationResource(ctx, kubernetesresource.CreateAuthorizationResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resource, Name: request.Name,
		Labels: request.Labels, Annotations: request.Annotations, AutomountServiceAccountToken: request.AutomountServiceAccountToken,
		Rules: request.Rules, Subjects: request.Subjects, RoleRef: request.RoleRef,
		DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondAuthorizationError(c, "create Kubernetes authorization resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesAuthorizationHandler) update(c *gin.Context) {
	resource, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("authorization_name"))
	if !ok {
		return
	}
	var request updateAuthorizationResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesAuthorizationMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization update request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes authorization mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateAuthorizationResource(ctx, kubernetesresource.UpdateAuthorizationResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resource, Name: c.Param("authorization_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion, AutomountServiceAccountToken: request.AutomountServiceAccountToken,
		Rules: request.Rules, Subjects: request.Subjects, DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondAuthorizationError(c, "update Kubernetes authorization resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesAuthorizationHandler) delete(c *gin.Context) {
	resource, target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("authorization_name"))
	if !ok {
		return
	}
	var request deleteAuthorizationResourceRequest
	if decodeJSONRequest(c, &request, maxKubernetesAuthorizationMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization delete request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes authorization mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteAuthorizationResource(ctx, kubernetesresource.DeleteAuthorizationResourceInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Resource: resource, Name: c.Param("authorization_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion, DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondAuthorizationError(c, "delete Kubernetes authorization resource", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesAuthorizationHandler) mutationTarget(c *gin.Context, action, name string) (kubernetesresource.AuthorizationResource, string, string, bool) {
	c.Header("Cache-Control", "no-store")
	resource := kubernetesresource.AuthorizationResource(c.Param("authorization_resource"))
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, resource, name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "authorization mutation does not accept query parameters")
		return resource, "", actor.User.ID, false
	}
	return resource, target, actor.User.ID, true
}

func (handler *kubernetesAuthorizationHandler) target(c *gin.Context, resource kubernetesresource.AuthorizationResource, name string) string {
	identity, _ := kubernetesresource.AuthorizationResourceIdentity(resource)
	return resourceTargetName(identity, c.Param("namespace_name"), name)
}

func (handler *kubernetesAuthorizationHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

func (handler *kubernetesAuthorizationHandler) respondAuthorizationError(c *gin.Context, operation string, err error) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseAuthorizationResourceListQuery(query url.Values) (kubernetesresource.ListAuthorizationResourcesInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListAuthorizationResourcesInput{}, err
	}
	result := kubernetesresource.ListAuthorizationResourcesInput{Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"), LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector")}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListAuthorizationResourcesInput{}, errors.New("invalid authorization list limit")
		}
		result.Limit = limit
	}
	return result, nil
}
