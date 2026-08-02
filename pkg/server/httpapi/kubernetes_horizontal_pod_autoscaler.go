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

const maxKubernetesHPARequestBytes = 512 * 1024

type kubernetesHorizontalPodAutoscalerService interface {
	ListHorizontalPodAutoscalers(context.Context, kubernetesresource.ListHorizontalPodAutoscalersInput) (kubernetesresource.HorizontalPodAutoscalerPage, error)
	GetHorizontalPodAutoscaler(context.Context, string, string, string) (kubernetesresource.HorizontalPodAutoscalerDetail, error)
	CreateHorizontalPodAutoscaler(context.Context, kubernetesresource.CreateHorizontalPodAutoscalerInput) (kubernetesresource.HorizontalPodAutoscalerDetail, error)
	UpdateHorizontalPodAutoscaler(context.Context, kubernetesresource.UpdateHorizontalPodAutoscalerInput) (kubernetesresource.HorizontalPodAutoscalerDetail, error)
	DeleteHorizontalPodAutoscaler(context.Context, kubernetesresource.DeleteHorizontalPodAutoscalerInput) error
}

type kubernetesHorizontalPodAutoscalerHandler struct {
	baseHandler
	service kubernetesHorizontalPodAutoscalerService
}

type createHorizontalPodAutoscalerRequest struct {
	Name        string                                         `json:"name"`
	Labels      map[string]string                              `json:"labels"`
	Annotations map[string]string                              `json:"annotations"`
	Spec        kubernetesresource.HorizontalPodAutoscalerSpec `json:"spec"`
	DryRun      bool                                           `json:"dry_run"`
	Confirm     bool                                           `json:"confirm"`
}

type updateHorizontalPodAutoscalerRequest struct {
	UID             string                                         `json:"uid"`
	ResourceVersion string                                         `json:"resource_version"`
	Spec            kubernetesresource.HorizontalPodAutoscalerSpec `json:"spec"`
	DryRun          bool                                           `json:"dry_run"`
	Confirm         bool                                           `json:"confirm"`
}

type deleteHorizontalPodAutoscalerRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesHorizontalPodAutoscalerHandler(
	logger *slog.Logger,
	service kubernetesHorizontalPodAutoscalerService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesHorizontalPodAutoscalerHandler {
	return &kubernetesHorizontalPodAutoscalerHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout), service: service,
	}
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseHorizontalPodAutoscalerListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid HorizontalPodAutoscaler query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler query is unavailable")
		return
	}
	input.ClusterID, input.Namespace = c.Param("cluster_id"), c.Param("namespace_name")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListHorizontalPodAutoscalers(ctx, input)
	cancel()
	if handler.respondError(c, "list Kubernetes HorizontalPodAutoscalers", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "HorizontalPodAutoscaler detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetHorizontalPodAutoscaler(
		ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("hpa_name"),
	)
	cancel()
	if handler.respondError(c, "get Kubernetes HorizontalPodAutoscaler", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) create(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createHorizontalPodAutoscalerRequest
	if decodeJSONRequest(c, &request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid HorizontalPodAutoscaler create request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateHorizontalPodAutoscaler(ctx, kubernetesresource.CreateHorizontalPodAutoscalerInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: request.Name,
		Labels: request.Labels, Annotations: request.Annotations, Spec: request.Spec,
		DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "create Kubernetes HorizontalPodAutoscaler", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"autoscaler": result, "dry_run": request.DryRun})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) update(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("hpa_name"))
	if !ok {
		return
	}
	var request updateHorizontalPodAutoscalerRequest
	if decodeJSONRequest(c, &request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid HorizontalPodAutoscaler update request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateHorizontalPodAutoscaler(ctx, kubernetesresource.UpdateHorizontalPodAutoscalerInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("hpa_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion, Spec: request.Spec,
		DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "update Kubernetes HorizontalPodAutoscaler", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"autoscaler": result, "dry_run": request.DryRun})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) delete(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("hpa_name"))
	if !ok {
		return
	}
	var request deleteHorizontalPodAutoscalerRequest
	if decodeJSONRequest(c, &request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid HorizontalPodAutoscaler delete request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteHorizontalPodAutoscaler(ctx, kubernetesresource.DeleteHorizontalPodAutoscalerInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("hpa_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion, DryRun: request.DryRun,
		Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "delete Kubernetes HorizontalPodAutoscaler", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) mutationTarget(c *gin.Context, action, name string) (string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "HorizontalPodAutoscaler mutation does not accept query parameters")
		return "", actor.User.ID, false
	}
	return target, actor.User.ID, true
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) target(c *gin.Context, name string) string {
	return resourceTargetName(kubernetesresource.HorizontalPodAutoscalerResourceIdentity(), c.Param("namespace_name"), name)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) respondError(c *gin.Context, operation string, err error) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseHorizontalPodAutoscalerListQuery(query url.Values) (kubernetesresource.ListHorizontalPodAutoscalersInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListHorizontalPodAutoscalersInput{}, err
	}
	result := kubernetesresource.ListHorizontalPodAutoscalersInput{
		Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListHorizontalPodAutoscalersInput{}, errors.New("invalid HorizontalPodAutoscaler list limit")
		}
		result.Limit = limit
	}
	return result, nil
}
