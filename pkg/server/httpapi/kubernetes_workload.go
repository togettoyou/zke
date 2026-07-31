package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const maxKubernetesWorkloadMutationRequestBytes = 512 * 1024

type kubernetesWorkloadService interface {
	ListWorkloads(
		context.Context,
		kubernetesresource.ListWorkloadsInput,
	) (kubernetesresource.WorkloadPage, error)
	GetWorkload(
		context.Context,
		string,
		string,
		kubernetesresource.WorkloadResource,
		string,
	) (kubernetesresource.WorkloadDetail, error)
	CreateWorkload(
		context.Context,
		kubernetesresource.CreateWorkloadInput,
	) (kubernetesresource.WorkloadDetail, error)
	ScaleWorkload(
		context.Context,
		kubernetesresource.ScaleWorkloadInput,
	) (kubernetesresource.WorkloadDetail, error)
	RestartWorkload(
		context.Context,
		kubernetesresource.WorkloadMutationInput,
	) (kubernetesresource.WorkloadDetail, error)
	SetWorkloadSuspension(
		context.Context,
		kubernetesresource.SetWorkloadSuspensionInput,
	) (kubernetesresource.WorkloadDetail, error)
	DeleteWorkload(
		context.Context,
		kubernetesresource.DeleteWorkloadInput,
	) error
}

type kubernetesWorkloadHandler struct {
	baseHandler
	service kubernetesWorkloadService
}

type workloadMutationRequest struct {
	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type workloadContainerTemplateRequest struct {
	Name            string `json:"name"`
	Image           string `json:"image"`
	ImagePullPolicy string `json:"image_pull_policy"`
}

type createWorkloadRequest struct {
	Name           string                             `json:"name"`
	Labels         map[string]string                  `json:"labels"`
	Containers     []workloadContainerTemplateRequest `json:"containers"`
	InitContainers []workloadContainerTemplateRequest `json:"init_containers"`

	Replicas    *int32 `json:"replicas"`
	ServiceName string `json:"service_name"`

	Parallelism             *int32 `json:"parallelism"`
	Completions             *int32 `json:"completions"`
	BackoffLimit            *int32 `json:"backoff_limit"`
	TTLSecondsAfterFinished *int32 `json:"ttl_seconds_after_finished"`

	Schedule                   string `json:"schedule"`
	TimeZone                   string `json:"time_zone"`
	Suspend                    *bool  `json:"suspend"`
	ConcurrencyPolicy          string `json:"concurrency_policy"`
	StartingDeadlineSeconds    *int64 `json:"starting_deadline_seconds"`
	SuccessfulJobsHistoryLimit *int32 `json:"successful_jobs_history_limit"`
	FailedJobsHistoryLimit     *int32 `json:"failed_jobs_history_limit"`

	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type scaleWorkloadRequest struct {
	Replicas int32 `json:"replicas"`
	workloadMutationRequest
}

type deleteWorkloadRequest struct {
	DryRun             bool   `json:"dry_run"`
	Confirm            bool   `json:"confirm"`
	UID                string `json:"uid"`
	ResourceVersion    string `json:"resource_version"`
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
	PropagationPolicy  string `json:"propagation_policy"`
}

func newKubernetesWorkloadHandler(
	logger *slog.Logger,
	service kubernetesWorkloadService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesWorkloadHandler {
	return &kubernetesWorkloadHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesWorkloadHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return
	}
	input, err := parseWorkloadListQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.Resource = resource
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListWorkloads(ctx, input)
	cancel()
	if handler.respondWorkloadError(c, "list Kubernetes workloads", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesWorkloadHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "workload detail does not accept query parameters")
		return
	}
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetWorkload(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		resource,
		c.Param("workload_name"),
	)
	cancel()
	if handler.respondWorkloadError(c, "get Kubernetes workload", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesWorkloadHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		handler.recordMutation(
			c,
			actor.User.ID,
			auditaction.KubernetesResourceCreate,
			c.Param("workload_resource")+" "+c.Param("namespace_name"),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return
	}
	resourceIdentity, _ := kubernetesresource.WorkloadResourceIdentity(resource)
	target := resourceTargetName(resourceIdentity, c.Param("namespace_name"), "")
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(
			c,
			actor.User.ID,
			auditaction.KubernetesResourceCreate,
			target,
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "workload creation does not accept query parameters")
		return
	}
	var request createWorkloadRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(
			c,
			actor.User.ID,
			auditaction.KubernetesResourceCreate,
			target,
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload create request")
		return
	}
	target = resourceTargetName(resourceIdentity, c.Param("namespace_name"), request.Name)
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceCreate,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateWorkload(
		ctx,
		kubernetesresource.CreateWorkloadInput{
			ClusterID:                  c.Param("cluster_id"),
			Namespace:                  c.Param("namespace_name"),
			Resource:                   resource,
			Name:                       request.Name,
			Labels:                     request.Labels,
			Containers:                 workloadContainerTemplates(request.Containers),
			InitContainers:             workloadContainerTemplates(request.InitContainers),
			Replicas:                   request.Replicas,
			ServiceName:                request.ServiceName,
			Parallelism:                request.Parallelism,
			Completions:                request.Completions,
			BackoffLimit:               request.BackoffLimit,
			TTLSecondsAfterFinished:    request.TTLSecondsAfterFinished,
			Schedule:                   request.Schedule,
			TimeZone:                   request.TimeZone,
			Suspend:                    request.Suspend,
			ConcurrencyPolicy:          request.ConcurrencyPolicy,
			StartingDeadlineSeconds:    request.StartingDeadlineSeconds,
			SuccessfulJobsHistoryLimit: request.SuccessfulJobsHistoryLimit,
			FailedJobsHistoryLimit:     request.FailedJobsHistoryLimit,
			DryRun:                     request.DryRun,
			Confirm:                    request.Confirm,
			IdempotencyKey:             c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	if err != nil {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
	}
	if handler.respondWorkloadError(c, "create Kubernetes workload", err) {
		return
	}
	handler.recordMutation(c, actor.User.ID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{
		"workload": result,
		"dry_run":  request.DryRun,
	})
}

func (handler *kubernetesWorkloadHandler) scale(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourcePatch,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request scaleWorkloadRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourcePatch, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload scale request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourcePatch,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ScaleWorkload(ctx, kubernetesresource.ScaleWorkloadInput{
		WorkloadMutationInput: handler.mutationInput(c, resource, request.workloadMutationRequest),
		Replicas:              request.Replicas,
	})
	cancel()
	handler.finishMutation(
		c,
		identity.User.ID,
		action,
		target,
		request.DryRun,
		result,
		err,
		"scale Kubernetes workload",
	)
}

func (handler *kubernetesWorkloadHandler) restart(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourcePatch,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request workloadMutationRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourcePatch, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload restart request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourcePatch,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.RestartWorkload(
		ctx,
		handler.mutationInput(c, resource, request),
	)
	cancel()
	handler.finishMutation(
		c,
		identity.User.ID,
		action,
		target,
		request.DryRun,
		result,
		err,
		"restart Kubernetes workload",
	)
}

func (handler *kubernetesWorkloadHandler) suspend(c *gin.Context) {
	handler.setSuspension(c, true)
}

func (handler *kubernetesWorkloadHandler) resume(c *gin.Context) {
	handler.setSuspension(c, false)
}

func (handler *kubernetesWorkloadHandler) setSuspension(c *gin.Context, suspended bool) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourcePatch,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request workloadMutationRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourcePatch, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid CronJob suspension request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourcePatch,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.SetWorkloadSuspension(
		ctx,
		kubernetesresource.SetWorkloadSuspensionInput{
			WorkloadMutationInput: handler.mutationInput(c, resource, request),
			Suspended:             suspended,
		},
	)
	cancel()
	handler.finishMutation(
		c,
		identity.User.ID,
		action,
		target,
		request.DryRun,
		result,
		err,
		"change Kubernetes CronJob suspension",
	)
}

func (handler *kubernetesWorkloadHandler) delete(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourceDelete,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request deleteWorkloadRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload delete request")
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
	if strings.TrimSpace(request.UID) == "" {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "workload UID precondition is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	propagation, ok := protocolDeletePropagation(request.PropagationPolicy)
	if !ok {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload deletion propagation policy")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteWorkload(ctx, kubernetesresource.DeleteWorkloadInput{
		WorkloadMutationInput: handler.mutationInput(c, resource, workloadMutationRequest{
			DryRun:  request.DryRun,
			Confirm: request.Confirm,
		}),
		GracePeriodSeconds: request.GracePeriodSeconds,
		Propagation:        propagation,
		UID:                request.UID,
		ResourceVersion:    request.ResourceVersion,
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondWorkloadError(c, "delete Kubernetes workload", err) {
		return
	}
	handler.recordMutation(c, identity.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"deleted": !request.DryRun,
		"dry_run": request.DryRun,
		"target":  target,
	})
}

func (handler *kubernetesWorkloadHandler) parseMutationTarget(
	c *gin.Context,
	action string,
) (kubernetesresource.WorkloadResource, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	resource, ok := kubernetesresource.ParseWorkloadResource(
		c.Param("workload_resource"),
	)
	if !ok {
		handler.recordMutation(
			c,
			actor.User.ID,
			action,
			c.Param("workload_resource")+" "+
				c.Param("namespace_name")+"/"+c.Param("workload_name"),
			"failed",
		)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload resource")
		return "", "", false
	}
	identity, _ := kubernetesresource.WorkloadResourceIdentity(resource)
	target := resourceTargetName(
		identity,
		c.Param("namespace_name"),
		c.Param("workload_name"),
	)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "workload mutation does not accept query parameters")
		return "", "", false
	}
	return resource, target, true
}

func (handler *kubernetesWorkloadHandler) mutationInput(
	c *gin.Context,
	resource kubernetesresource.WorkloadResource,
	request workloadMutationRequest,
) kubernetesresource.WorkloadMutationInput {
	return kubernetesresource.WorkloadMutationInput{
		ClusterID:      c.Param("cluster_id"),
		Namespace:      c.Param("namespace_name"),
		Resource:       resource,
		Name:           c.Param("workload_name"),
		DryRun:         request.DryRun,
		Confirm:        request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	}
}

func workloadContainerTemplates(
	input []workloadContainerTemplateRequest,
) []kubernetesresource.WorkloadContainerTemplate {
	result := make([]kubernetesresource.WorkloadContainerTemplate, 0, len(input))
	for _, container := range input {
		result = append(result, kubernetesresource.WorkloadContainerTemplate{
			Name:            container.Name,
			Image:           container.Image,
			ImagePullPolicy: container.ImagePullPolicy,
		})
	}
	return result
}

func (handler *kubernetesWorkloadHandler) finishMutation(
	c *gin.Context,
	actorUserID string,
	action string,
	target string,
	dryRun bool,
	result kubernetesresource.WorkloadDetail,
	err error,
	operation string,
) {
	if err != nil {
		handler.recordMutation(c, actorUserID, action, target, "failed")
	}
	if handler.respondWorkloadError(c, operation, err) {
		return
	}
	handler.recordMutation(c, actorUserID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"workload": result,
		"dry_run":  dryRun,
	})
}

func (handler *kubernetesWorkloadHandler) recordMutation(
	c *gin.Context,
	actorUserID string,
	action string,
	target string,
	result string,
) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorUserID, action, target, result)
}

func parseWorkloadListQuery(query url.Values) (kubernetesresource.ListWorkloadsInput, error) {
	allowed := map[string]struct{}{
		"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListWorkloadsInput{}, err
	}
	result := kubernetesresource.ListWorkloadsInput{
		Limit:         kubernetesresource.DefaultResourceListLimit,
		ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"),
		FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListWorkloadsInput{}, errors.New("invalid workload list limit")
		}
		result.Limit = limit
	}
	return result, nil
}

func (handler *kubernetesWorkloadHandler) respondWorkloadError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}
