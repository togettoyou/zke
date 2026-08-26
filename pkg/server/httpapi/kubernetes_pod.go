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

const maxKubernetesPodMutationRequestBytes = 64 * 1024

var kubernetesPodResourceIdentity = kubernetesresource.ResourceIdentity{
	Version:  "v1",
	Resource: "pods",
}

type kubernetesPodService interface {
	ListPods(context.Context, kubernetesresource.ListPodsInput) (kubernetesresource.PodPage, error)
	GetPod(context.Context, string, string, string) (kubernetesresource.PodDetail, error)
	DeletePod(context.Context, kubernetesresource.DeletePodInput) error
	EvictPod(context.Context, kubernetesresource.EvictPodInput) (kubernetesresource.EvictPodResult, error)
}

type kubernetesPodHandler struct {
	baseHandler
	service kubernetesPodService
}

type deleteKubernetesPodRequest struct {
	DryRun             bool   `json:"dry_run"`
	Confirm            bool   `json:"confirm"`
	UID                string `json:"uid"`
	ResourceVersion    string `json:"resource_version"`
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
	PropagationPolicy  string `json:"propagation_policy"`
}

// An eviction names the object it removes and nothing else. There is no
// propagation policy — the eviction subresource decides that — and no
// resourceVersion, because a Pod that changed since the listing is still the same
// Pod as long as its UID matches, and re-reading it would not change the
// decision to take it off its Node.
type evictKubernetesPodRequest struct {
	DryRun             bool   `json:"dry_run"`
	Confirm            bool   `json:"confirm"`
	UID                string `json:"uid"`
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
}

func newKubernetesPodHandler(
	logger *slog.Logger,
	service kubernetesPodService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesPodHandler {
	return &kubernetesPodHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesPodHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parsePodListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListPods(ctx, input)
	cancel()
	if handler.respondPodError(c, "list Kubernetes Pods", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesPodHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Pod detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetPod(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		c.Param("pod_name"),
	)
	cancel()
	if handler.respondPodError(c, "get Kubernetes Pod", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesPodHandler) delete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(
		kubernetesPodResourceIdentity,
		c.Param("namespace_name"),
		c.Param("pod_name"),
	)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Pod deletion does not accept query parameters")
		return
	}
	var request deleteKubernetesPodRequest
	if decodeJSONRequest(c, &request, maxKubernetesPodMutationRequestBytes) != nil {
		handler.recordMutation(c, actor.User.ID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod delete request")
		return
	}
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceDelete, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	propagation, ok := protocolDeletePropagation(request.PropagationPolicy)
	if !ok || request.UID == "" {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod delete request")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeletePod(ctx, kubernetesresource.DeletePodInput{
		ClusterID:          c.Param("cluster_id"),
		Namespace:          c.Param("namespace_name"),
		Name:               c.Param("pod_name"),
		UID:                request.UID,
		ResourceVersion:    request.ResourceVersion,
		GracePeriodSeconds: request.GracePeriodSeconds,
		Propagation:        propagation,
		DryRun:             request.DryRun,
		Confirm:            request.Confirm,
		IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
	}
	if handler.respondPodError(c, "delete Kubernetes Pod", err) {
		return
	}
	handler.recordMutation(c, actor.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"deleted": !request.DryRun,
		"dry_run": request.DryRun,
		"target":  target,
	})
}

// evict removes one Pod through the Kubernetes eviction subresource.
//
// It sits beside delete rather than replacing it, and answers to the same
// `cluster.resource.delete`: both take a running Pod away, and the eviction is
// the one that lets the Cluster's PodDisruptionBudgets refuse. A refusal comes
// back as its own error so the operator reads "a budget stopped this" instead of
// a generic conflict they would answer by reloading the page.
func (handler *kubernetesPodHandler) evict(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(
		kubernetesPodResourceIdentity,
		c.Param("namespace_name"),
		c.Param("pod_name"),
	)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, auditaction.KubernetesPodEvict, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Pod eviction does not accept query parameters")
		return
	}
	var request evictKubernetesPodRequest
	if decodeJSONRequest(c, &request, maxKubernetesPodMutationRequestBytes) != nil {
		handler.recordMutation(c, actor.User.ID, auditaction.KubernetesPodEvict, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod eviction request")
		return
	}
	action := auditaction.KubernetesPodEvict
	if request.DryRun {
		action = auditaction.KubernetesPodEvictDryRun
	}
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if request.UID == "" {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod eviction request")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.EvictPod(ctx, kubernetesresource.EvictPodInput{
		ClusterID:          c.Param("cluster_id"),
		Namespace:          c.Param("namespace_name"),
		Name:               c.Param("pod_name"),
		UID:                request.UID,
		GracePeriodSeconds: request.GracePeriodSeconds,
		DryRun:             request.DryRun,
		Confirm:            request.Confirm,
		IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
	}
	if handler.respondEvictionError(c, err) {
		return
	}
	handler.recordMutation(c, actor.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"eviction": result,
		"dry_run":  request.DryRun,
		"target":   target,
	})
}

func parsePodListQuery(query url.Values) (kubernetesresource.ListPodsInput, error) {
	allowed := map[string]struct{}{
		"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListPodsInput{}, err
	}
	result := kubernetesresource.ListPodsInput{
		Limit:         kubernetesresource.DefaultResourceListLimit,
		ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"),
		FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListPodsInput{}, errors.New("invalid Pod list limit")
		}
		result.Limit = limit
	}
	return result, nil
}

func (handler *kubernetesPodHandler) respondPodError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

// A blocked eviction is 409: the request was well formed and understood, and
// the Cluster refused it because of a rule the caller can go and read. It is not
// 429 — nothing here is rate limiting, and a client that retries on its own
// would keep asking a question already answered.
func (handler *kubernetesPodHandler) respondEvictionError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrPodEvictionBlocked) {
		return handler.respondError(c, "evict Kubernetes Pod", err, errorMapping{
			kubernetesresource.ErrPodEvictionBlocked,
			http.StatusConflict,
			"pod_disruption_budget_blocked",
			"a PodDisruptionBudget refused the eviction",
		})
	}
	return handler.respondPodError(c, "evict Kubernetes Pod", err)
}

func (handler *kubernetesPodHandler) recordMutation(
	c *gin.Context,
	actorUserID string,
	action string,
	target string,
	result string,
) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorUserID, action, target, result)
}
