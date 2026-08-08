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
	GetHorizontalPodAutoscalerMetricTrend(context.Context, string, string, string) (kubernetesresource.HPAMetricTrend, error)
	ListVerticalPodAutoscalers(context.Context, kubernetesresource.AutoscalingExtensionListInput) (kubernetesresource.VPAPage, error)
	GetVerticalPodAutoscaler(context.Context, string, string, string) (kubernetesresource.VPADetail, error)
	CreateVerticalPodAutoscaler(context.Context, kubernetesresource.CreateVPAInput) (kubernetesresource.VPADetail, error)
	UpdateVerticalPodAutoscaler(context.Context, kubernetesresource.UpdateVPAInput) (kubernetesresource.VPADetail, error)
	DeleteVerticalPodAutoscaler(context.Context, kubernetesresource.DeleteAutoscalingExtensionInput) error
	ListKEDAScaledObjects(context.Context, kubernetesresource.AutoscalingExtensionListInput) (kubernetesresource.KEDAScaledObjectPage, error)
	GetKEDAScaledObject(context.Context, string, string, string) (kubernetesresource.KEDAScaledObjectDetail, error)
	CreateKEDAScaledObject(context.Context, kubernetesresource.CreateKEDAScaledObjectInput) (kubernetesresource.KEDAScaledObjectDetail, error)
	UpdateKEDAScaledObject(context.Context, kubernetesresource.UpdateKEDAScaledObjectInput) (kubernetesresource.KEDAScaledObjectDetail, error)
	DeleteKEDAScaledObject(context.Context, kubernetesresource.DeleteAutoscalingExtensionInput) error
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

type createVPARequest struct {
	Name        string                     `json:"name"`
	Labels      map[string]string          `json:"labels"`
	Annotations map[string]string          `json:"annotations"`
	Spec        kubernetesresource.VPASpec `json:"spec"`
	DryRun      bool                       `json:"dry_run"`
	Confirm     bool                       `json:"confirm"`
}

type updateVPARequest struct {
	UID             string                     `json:"uid"`
	ResourceVersion string                     `json:"resource_version"`
	Spec            kubernetesresource.VPASpec `json:"spec"`
	DryRun          bool                       `json:"dry_run"`
	Confirm         bool                       `json:"confirm"`
}

type createKEDAScaledObjectRequest struct {
	Name        string                                  `json:"name"`
	Labels      map[string]string                       `json:"labels"`
	Annotations map[string]string                       `json:"annotations"`
	Spec        kubernetesresource.KEDAScaledObjectSpec `json:"spec"`
	DryRun      bool                                    `json:"dry_run"`
	Confirm     bool                                    `json:"confirm"`
}

type updateKEDAScaledObjectRequest struct {
	UID             string                                  `json:"uid"`
	ResourceVersion string                                  `json:"resource_version"`
	Spec            kubernetesresource.KEDAScaledObjectSpec `json:"spec"`
	DryRun          bool                                    `json:"dry_run"`
	Confirm         bool                                    `json:"confirm"`
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

func (handler *kubernetesHorizontalPodAutoscalerHandler) metricTrend(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "HorizontalPodAutoscaler metric trend does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "HorizontalPodAutoscaler metric trend is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetHorizontalPodAutoscalerMetricTrend(ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("hpa_name"))
	cancel()
	if handler.respondError(c, "get Kubernetes HorizontalPodAutoscaler metric trend", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) listVPA(c *gin.Context) {
	input, ok := handler.extensionListInput(c, "VerticalPodAutoscaler")
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListVerticalPodAutoscalers(ctx, input)
	cancel()
	if handler.respondError(c, "list Kubernetes VerticalPodAutoscalers", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) getVPA(c *gin.Context) {
	handler.getExtension(c, "VerticalPodAutoscaler", func(ctx context.Context) (any, error) {
		return handler.service.GetVerticalPodAutoscaler(ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("vpa_name"))
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) createVPA(c *gin.Context) {
	var request createVPARequest
	handler.createExtension(c, kubernetesresource.VerticalPodAutoscalerResourceIdentity(), "VerticalPodAutoscaler", &request, func(ctx context.Context) (any, bool, error) {
		result, err := handler.service.CreateVerticalPodAutoscaler(ctx, kubernetesresource.CreateVPAInput{ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: request.Name, Labels: request.Labels, Annotations: request.Annotations, Spec: request.Spec, DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName)})
		return result, request.DryRun, err
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) updateVPA(c *gin.Context) {
	var request updateVPARequest
	handler.updateExtension(c, kubernetesresource.VerticalPodAutoscalerResourceIdentity(), "VerticalPodAutoscaler", "vpa_name", &request, func(ctx context.Context) (any, error) {
		return handler.service.UpdateVerticalPodAutoscaler(ctx, kubernetesresource.UpdateVPAInput{ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("vpa_name"), UID: request.UID, ResourceVersion: request.ResourceVersion, Spec: request.Spec, DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName)})
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) deleteVPA(c *gin.Context) {
	handler.deleteExtension(c, kubernetesresource.VerticalPodAutoscalerResourceIdentity(), "VerticalPodAutoscaler", "vpa_name", func(ctx context.Context, input kubernetesresource.DeleteAutoscalingExtensionInput) error {
		return handler.service.DeleteVerticalPodAutoscaler(ctx, input)
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) listKEDA(c *gin.Context) {
	input, ok := handler.extensionListInput(c, "KEDA ScaledObject")
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListKEDAScaledObjects(ctx, input)
	cancel()
	if handler.respondError(c, "list KEDA ScaledObjects", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) getKEDA(c *gin.Context) {
	handler.getExtension(c, "KEDA ScaledObject", func(ctx context.Context) (any, error) {
		return handler.service.GetKEDAScaledObject(ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("scaled_object_name"))
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) createKEDA(c *gin.Context) {
	var request createKEDAScaledObjectRequest
	handler.createExtension(c, kubernetesresource.KEDAScaledObjectResourceIdentity(), "KEDA ScaledObject", &request, func(ctx context.Context) (any, bool, error) {
		result, err := handler.service.CreateKEDAScaledObject(ctx, kubernetesresource.CreateKEDAScaledObjectInput{ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: request.Name, Labels: request.Labels, Annotations: request.Annotations, Spec: request.Spec, DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName)})
		return result, request.DryRun, err
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) updateKEDA(c *gin.Context) {
	var request updateKEDAScaledObjectRequest
	handler.updateExtension(c, kubernetesresource.KEDAScaledObjectResourceIdentity(), "KEDA ScaledObject", "scaled_object_name", &request, func(ctx context.Context) (any, error) {
		return handler.service.UpdateKEDAScaledObject(ctx, kubernetesresource.UpdateKEDAScaledObjectInput{ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("scaled_object_name"), UID: request.UID, ResourceVersion: request.ResourceVersion, Spec: request.Spec, DryRun: request.DryRun, Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName)})
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) deleteKEDA(c *gin.Context) {
	handler.deleteExtension(c, kubernetesresource.KEDAScaledObjectResourceIdentity(), "KEDA ScaledObject", "scaled_object_name", func(ctx context.Context, input kubernetesresource.DeleteAutoscalingExtensionInput) error {
		return handler.service.DeleteKEDAScaledObject(ctx, input)
	})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) extensionListInput(c *gin.Context, kind string) (kubernetesresource.AutoscalingExtensionListInput, bool) {
	c.Header("Cache-Control", "no-store")
	query, err := parseHorizontalPodAutoscalerListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid "+kind+" query")
		return kubernetesresource.AutoscalingExtensionListInput{}, false
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", kind+" query is unavailable")
		return kubernetesresource.AutoscalingExtensionListInput{}, false
	}
	return kubernetesresource.AutoscalingExtensionListInput{
		ClusterID:     c.Param("cluster_id"),
		Namespace:     c.Param("namespace_name"),
		Limit:         query.Limit,
		ContinueToken: query.ContinueToken,
		LabelSelector: query.LabelSelector,
		FieldSelector: query.FieldSelector,
	}, true
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) getExtension(c *gin.Context, kind string, get func(context.Context) (any, error)) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", kind+" detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", kind+" query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := get(ctx)
	cancel()
	if handler.respondError(c, "get Kubernetes "+kind, err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) createExtension(
	c *gin.Context,
	identity kubernetesresource.ResourceIdentity,
	kind string,
	request any,
	create func(context.Context) (any, bool, error),
) {
	target, actorID, ok := handler.extensionMutationTarget(c, identity, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	if decodeJSONRequest(c, request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid "+kind+" create request")
		return
	}
	name, dryRun, confirm := extensionCreateRequestValues(request)
	target = resourceTargetName(identity, c.Param("namespace_name"), name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, dryRun)
	if !dryRun && !confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", kind+" mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, resultDryRun, err := create(ctx)
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "create Kubernetes "+kind, err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if resultDryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"autoscaler": result, "dry_run": resultDryRun})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) updateExtension(
	c *gin.Context,
	identity kubernetesresource.ResourceIdentity,
	kind, parameter string,
	request any,
	update func(context.Context) (any, error),
) {
	target, actorID, ok := handler.extensionMutationTarget(c, identity, auditaction.KubernetesResourceUpdate, c.Param(parameter))
	if !ok {
		return
	}
	if decodeJSONRequest(c, request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid "+kind+" update request")
		return
	}
	dryRun, confirm := extensionUpdateRequestValues(request)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceUpdate, dryRun)
	if !dryRun && !confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", kind+" mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := update(ctx)
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "update Kubernetes "+kind, err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"autoscaler": result, "dry_run": dryRun})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) deleteExtension(
	c *gin.Context,
	identity kubernetesresource.ResourceIdentity,
	kind, parameter string,
	remove func(context.Context, kubernetesresource.DeleteAutoscalingExtensionInput) error,
) {
	target, actorID, ok := handler.extensionMutationTarget(c, identity, auditaction.KubernetesResourceDelete, c.Param(parameter))
	if !ok {
		return
	}
	var request deleteHorizontalPodAutoscalerRequest
	if decodeJSONRequest(c, &request, maxKubernetesHPARequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid "+kind+" delete request")
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", kind+" mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := remove(ctx, kubernetesresource.DeleteAutoscalingExtensionInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param(parameter),
		UID: request.UID, ResourceVersion: request.ResourceVersion, DryRun: request.DryRun,
		Confirm: request.Confirm, IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondError(c, "delete Kubernetes "+kind, err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesHorizontalPodAutoscalerHandler) extensionMutationTarget(c *gin.Context, identity kubernetesresource.ResourceIdentity, action, name string) (string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(identity, c.Param("namespace_name"), name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "autoscaler mutation does not accept query parameters")
		return "", actor.User.ID, false
	}
	return target, actor.User.ID, true
}

func extensionCreateRequestValues(request any) (string, bool, bool) {
	switch value := request.(type) {
	case *createVPARequest:
		return value.Name, value.DryRun, value.Confirm
	case *createKEDAScaledObjectRequest:
		return value.Name, value.DryRun, value.Confirm
	default:
		return "", false, false
	}
}

func extensionUpdateRequestValues(request any) (bool, bool) {
	switch value := request.(type) {
	case *updateVPARequest:
		return value.DryRun, value.Confirm
	case *updateKEDAScaledObjectRequest:
		return value.DryRun, value.Confirm
	default:
		return false, false
	}
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
