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
	UpdateWorkload(
		context.Context,
		kubernetesresource.UpdateWorkloadInput,
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
	ListWorkloadRevisions(
		context.Context,
		kubernetesresource.ListWorkloadRevisionsInput,
	) (kubernetesresource.WorkloadRevisionPage, error)
	RollbackWorkload(
		context.Context,
		kubernetesresource.RollbackWorkloadInput,
	) (kubernetesresource.WorkloadDetail, error)
	DeleteWorkload(
		context.Context,
		kubernetesresource.DeleteWorkloadInput,
	) error
	TriggerCronJob(
		context.Context,
		kubernetesresource.TriggerCronJobInput,
	) (kubernetesresource.WorkloadDetail, error)
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
	Name            string                                     `json:"name"`
	Image           string                                     `json:"image"`
	ImagePullPolicy string                                     `json:"image_pull_policy"`
	Command         []string                                   `json:"command"`
	Args            []string                                   `json:"args"`
	WorkingDir      string                                     `json:"working_dir"`
	Env             []workloadEnvVarRequest                    `json:"env"`
	Resources       *workloadResourceRequirementsRequest       `json:"resources"`
	VolumeMounts    []workloadVolumeMountRequest               `json:"volume_mounts"`
	LivenessProbe   *workloadProbeRequest                      `json:"liveness_probe"`
	ReadinessProbe  *workloadProbeRequest                      `json:"readiness_probe"`
	Lifecycle       *workloadLifecycleRequest                  `json:"lifecycle"`
	Privileged      *bool                                      `json:"privileged"`
	Ports           []kubernetesresource.WorkloadContainerPort `json:"ports"`
}

// The modeled fields, submitted the same way by a create and an update. An
// embedded struct is flattened by encoding/json, so the wire shape is the flat
// object both endpoints document.
type workloadSpecRequest struct {
	Labels         map[string]string                  `json:"labels"`
	Annotations    map[string]string                  `json:"annotations"`
	Description    string                             `json:"description"`
	Containers     []workloadContainerTemplateRequest `json:"containers"`
	InitContainers []workloadContainerTemplateRequest `json:"init_containers"`

	Volumes                   []workloadVolumeRequest                               `json:"volumes"`
	ImagePullSecrets          []string                                              `json:"image_pull_secrets"`
	NodeSelector              map[string]string                                     `json:"node_selector"`
	Tolerations               []workloadTolerationRequest                           `json:"tolerations"`
	Affinity                  *kubernetesresource.WorkloadAffinity                  `json:"affinity"`
	TopologySpreadConstraints []kubernetesresource.WorkloadTopologySpreadConstraint `json:"topology_spread_constraints"`

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
}

type createWorkloadRequest struct {
	Name string `json:"name"`
	workloadSpecRequest

	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

type updateWorkloadRequest struct {
	// The object the edit started from. A mismatch is a conflict, not an
	// overwrite: the form was filled in against a version that is no longer
	// current, and whatever changed in between is not in it.
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	workloadSpecRequest

	DryRun  bool `json:"dry_run"`
	Confirm bool `json:"confirm"`
}

func workloadSpecInput(request workloadSpecRequest) kubernetesresource.WorkloadSpecInput {
	return kubernetesresource.WorkloadSpecInput{
		Labels:                     request.Labels,
		Annotations:                request.Annotations,
		Description:                request.Description,
		Containers:                 workloadContainerTemplates(request.Containers),
		InitContainers:             workloadContainerTemplates(request.InitContainers),
		Volumes:                    workloadVolumes(request.Volumes),
		ImagePullSecrets:           request.ImagePullSecrets,
		NodeSelector:               request.NodeSelector,
		Tolerations:                workloadTolerations(request.Tolerations),
		Affinity:                   request.Affinity,
		TopologySpreadConstraints:  request.TopologySpreadConstraints,
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
	}
}

type scaleWorkloadRequest struct {
	Replicas int32 `json:"replicas"`
	workloadMutationRequest
}

// A rollback names the revision it restores and the version of the workload it
// was chosen against. Both preconditions are required, as they are for an
// update: the revision list is read at one version of the object, and a rollback
// confirmed after someone else has changed it is a decision about a different
// object.
type rollbackWorkloadRequest struct {
	Revision        int64  `json:"revision"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	workloadMutationRequest
}

// Running a CronJob now names the CronJob it runs, by UID. A schedule and a Pod
// template are exactly what changes between the moment an operator opens the
// page and the moment they press the button, and a run confirmed against the
// old one would start work nobody reviewed.
type triggerCronJobRequest struct {
	UID string `json:"uid"`
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
			ClusterID:         c.Param("cluster_id"),
			Namespace:         c.Param("namespace_name"),
			Resource:          resource,
			Name:              request.Name,
			WorkloadSpecInput: workloadSpecInput(request.workloadSpecRequest),
			DryRun:            request.DryRun,
			Confirm:           request.Confirm,
			IdempotencyKey:    c.GetHeader(idempotencyKeyHeaderName),
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

// trigger creates one Job from a CronJob's template, off schedule.
//
// It answers to `cluster.resource.create` rather than to the update permission
// the other CronJob actions use, and the route is registered accordingly: this
// creates an object, and nothing about the CronJob changes.
func (handler *kubernetesWorkloadHandler) trigger(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesCronJobTrigger,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	if resource != kubernetesresource.WorkloadCronJobs {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesCronJobTrigger, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "only a CronJob can be run on demand")
		return
	}
	var request triggerCronJobRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesCronJobTrigger, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid CronJob run request")
		return
	}
	action := auditaction.KubernetesCronJobTrigger
	if request.DryRun {
		action = auditaction.KubernetesCronJobTriggerDryRun
	}
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if request.UID == "" {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid CronJob run request")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.TriggerCronJob(ctx, kubernetesresource.TriggerCronJobInput{
		ClusterID:      c.Param("cluster_id"),
		Namespace:      c.Param("namespace_name"),
		Name:           c.Param("workload_name"),
		UID:            request.UID,
		DryRun:         request.DryRun,
		Confirm:        request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondCronJobTriggerError(c, err) {
		return
	}
	handler.recordMutation(c, identity.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"workload": result,
		"dry_run":  request.DryRun,
	})
}

// The two refusals this endpoint makes on its own are worth their own codes: a
// suspended CronJob is answered by resuming it, and a name with no room for the
// run suffix is answered by renaming the CronJob. Neither is fixed by retrying.
func (handler *kubernetesWorkloadHandler) respondCronJobTriggerError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrCronJobSuspended) ||
		errors.Is(err, kubernetesresource.ErrCronJobJobNameTooLong) {
		return handler.respondError(c, "run Kubernetes CronJob", err,
			errorMapping{
				kubernetesresource.ErrCronJobSuspended,
				http.StatusConflict,
				"cron_job_suspended",
				"CronJob is suspended; resume it before running it on demand",
			},
			errorMapping{
				kubernetesresource.ErrCronJobJobNameTooLong,
				http.StatusBadRequest,
				"cron_job_name_too_long",
				"CronJob name leaves no room for the generated Job name",
			},
		)
	}
	return handler.respondWorkloadError(c, "run Kubernetes CronJob", err)
}

func (handler *kubernetesWorkloadHandler) revisions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "workload revisions do not accept query parameters")
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
	result, err := handler.service.ListWorkloadRevisions(
		ctx,
		kubernetesresource.ListWorkloadRevisionsInput{
			ClusterID: c.Param("cluster_id"),
			Namespace: c.Param("namespace_name"),
			Resource:  resource,
			Name:      c.Param("workload_name"),
		},
	)
	cancel()
	if handler.respondWorkloadError(c, "list Kubernetes workload revisions", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesWorkloadHandler) rollback(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourceUpdate,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request rollbackWorkloadRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload rollback request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if strings.TrimSpace(request.UID) == "" ||
		strings.TrimSpace(request.ResourceVersion) == "" {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"workload UID and resourceVersion preconditions are required",
		)
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.RollbackWorkload(
		ctx,
		kubernetesresource.RollbackWorkloadInput{
			WorkloadMutationInput: handler.mutationInput(
				c,
				resource,
				request.workloadMutationRequest,
			),
			Revision:        request.Revision,
			UID:             request.UID,
			ResourceVersion: request.ResourceVersion,
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
		"roll back Kubernetes workload",
	)
}

func (handler *kubernetesWorkloadHandler) update(c *gin.Context) {
	resource, target, ok := handler.parseMutationTarget(
		c,
		auditaction.KubernetesResourceUpdate,
	)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	var request updateWorkloadRequest
	if decodeJSONRequest(c, &request, maxKubernetesWorkloadMutationRequestBytes) != nil {
		handler.recordMutation(c, identity.User.ID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid workload update request")
		return
	}
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		request.DryRun,
	)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	// Both preconditions, always: an edit is filled in against one version of an
	// object, and without them a slow form would overwrite whatever landed while
	// it was open.
	if strings.TrimSpace(request.UID) == "" || strings.TrimSpace(request.ResourceVersion) == "" {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"workload UID and resourceVersion preconditions are required",
		)
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "workload mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateWorkload(
		ctx,
		kubernetesresource.UpdateWorkloadInput{
			ClusterID:         c.Param("cluster_id"),
			Namespace:         c.Param("namespace_name"),
			Resource:          resource,
			Name:              c.Param("workload_name"),
			UID:               request.UID,
			ResourceVersion:   request.ResourceVersion,
			WorkloadSpecInput: workloadSpecInput(request.workloadSpecRequest),
			DryRun:            request.DryRun,
			Confirm:           request.Confirm,
			IdempotencyKey:    c.GetHeader(idempotencyKeyHeaderName),
		},
	)
	cancel()
	if err != nil {
		handler.recordMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondWorkloadError(c, "update Kubernetes workload", err) {
		return
	}
	handler.recordMutation(c, identity.User.ID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{
		"workload": result,
		"dry_run":  request.DryRun,
	})
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
			Command:         container.Command,
			Args:            container.Args,
			WorkingDir:      container.WorkingDir,
			Env:             workloadEnvVars(container.Env),
			Resources:       workloadResourceRequirements(container.Resources),
			VolumeMounts:    workloadVolumeMounts(container.VolumeMounts),
			LivenessProbe:   workloadProbe(container.LivenessProbe),
			ReadinessProbe:  workloadProbe(container.ReadinessProbe),
			Lifecycle:       workloadLifecycle(container.Lifecycle),
			Privileged:      container.Privileged,
			Ports:           container.Ports,
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
	if err != nil {
		// Checked before the shared mappings so that the three revision failures
		// keep their own codes: without this they would all fall through to
		// `invalid_request` or `cluster_api_conflict`, and the Console could not
		// tell "this type has no history" from "you sent something malformed".
		for _, mapping := range workloadRevisionErrorMappings() {
			if errors.Is(err, mapping.target) {
				writeError(c, mapping.status, mapping.code, mapping.message)
				return true
			}
		}
	}
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func workloadRevisionErrorMappings() []errorMapping {
	return []errorMapping{
		// 400 rather than 404: the workload exists and the route is right, but a
		// Job or a CronJob has no revision history for any request to name.
		{
			kubernetesresource.ErrWorkloadRevisionsUnsupported,
			http.StatusBadRequest,
			"workload_revisions_unsupported",
			"workload type keeps no revision history",
		},
		{
			kubernetesresource.ErrWorkloadRevisionNotFound,
			http.StatusNotFound,
			"workload_revision_not_found",
			"requested workload revision does not exist",
		},
		// 409 rather than 400: the request was well formed and the revision does
		// exist — it is the one already running, so there is nothing to roll back.
		{
			kubernetesresource.ErrWorkloadRevisionUnchanged,
			http.StatusConflict,
			"workload_revision_unchanged",
			"workload already runs the selected revision",
		},
	}
}
