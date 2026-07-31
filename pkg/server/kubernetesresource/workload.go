package kubernetesresource

import (
	"context"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

type WorkloadResource string

const (
	WorkloadDeployments  WorkloadResource = "deployments"
	WorkloadStatefulSets WorkloadResource = "statefulsets"
	WorkloadDaemonSets   WorkloadResource = "daemonsets"
	WorkloadJobs         WorkloadResource = "jobs"
	WorkloadCronJobs     WorkloadResource = "cronjobs"
)

type ListWorkloadsInput struct {
	ClusterID     string
	Namespace     string
	Resource      WorkloadResource
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type WorkloadPage struct {
	Workloads          []WorkloadSummary `json:"workloads"`
	ContinueToken      string            `json:"continue_token"`
	ResourceVersion    string            `json:"resource_version"`
	RemainingItemCount *int64            `json:"remaining_item_count"`
}

type WorkloadSummary struct {
	Resource           WorkloadResource       `json:"resource"`
	APIVersion         string                 `json:"api_version"`
	Kind               string                 `json:"kind"`
	Namespace          string                 `json:"namespace"`
	Name               string                 `json:"name"`
	UID                string                 `json:"uid"`
	ResourceVersion    string                 `json:"resource_version"`
	CreationTimestamp  time.Time              `json:"creation_timestamp"`
	Generation         int64                  `json:"generation"`
	ObservedGeneration int64                  `json:"observed_generation"`
	Labels             map[string]string      `json:"labels"`
	Status             string                 `json:"status"`
	Images             []string               `json:"images"`
	Replicas           *WorkloadReplicaStatus `json:"replicas,omitempty"`
	Job                *WorkloadJobStatus     `json:"job,omitempty"`
	CronJob            *WorkloadCronJobStatus `json:"cron_job,omitempty"`
}

type WorkloadReplicaStatus struct {
	Desired     int32 `json:"desired"`
	Current     int32 `json:"current"`
	Updated     int32 `json:"updated"`
	Ready       int32 `json:"ready"`
	Available   int32 `json:"available"`
	Unavailable int32 `json:"unavailable"`
}

type WorkloadJobStatus struct {
	Parallelism    *int32     `json:"parallelism,omitempty"`
	Completions    *int32     `json:"completions,omitempty"`
	Active         int32      `json:"active"`
	Succeeded      int32      `json:"succeeded"`
	Failed         int32      `json:"failed"`
	StartTime      *time.Time `json:"start_time,omitempty"`
	CompletionTime *time.Time `json:"completion_time,omitempty"`
}

type WorkloadCronJobStatus struct {
	Schedule           string     `json:"schedule"`
	Suspend            bool       `json:"suspend"`
	Active             int        `json:"active"`
	LastScheduleTime   *time.Time `json:"last_schedule_time,omitempty"`
	LastSuccessfulTime *time.Time `json:"last_successful_time,omitempty"`
}

type WorkloadDetail struct {
	WorkloadSummary
	Annotations          map[string]string   `json:"annotations"`
	Selector             *WorkloadSelector   `json:"selector,omitempty"`
	Containers           []WorkloadContainer `json:"containers"`
	InitContainers       []WorkloadContainer `json:"init_containers"`
	Conditions           []WorkloadCondition `json:"conditions"`
	Strategy             string              `json:"strategy"`
	MinReadySeconds      int32               `json:"min_ready_seconds"`
	RevisionHistoryLimit *int32              `json:"revision_history_limit,omitempty"`
	ServiceName          string              `json:"service_name,omitempty"`
	CompletionMode       string              `json:"completion_mode,omitempty"`
	ConcurrencyPolicy    string              `json:"concurrency_policy,omitempty"`
}

type WorkloadSelector struct {
	MatchLabels      map[string]string             `json:"match_labels"`
	MatchExpressions []WorkloadSelectorRequirement `json:"match_expressions"`
}

type WorkloadSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type WorkloadContainer struct {
	Name            string `json:"name"`
	Image           string `json:"image"`
	ImagePullPolicy string `json:"image_pull_policy"`
}

type WorkloadCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

func ParseWorkloadResource(value string) (WorkloadResource, bool) {
	resource := WorkloadResource(value)
	_, exists := workloadIdentities[resource]
	return resource, exists
}

func WorkloadResourceIdentity(resource WorkloadResource) (ResourceIdentity, bool) {
	identity, exists := workloadIdentities[resource]
	return identity, exists
}

var workloadIdentities = map[WorkloadResource]ResourceIdentity{
	WorkloadDeployments: {
		Group: "apps", Version: "v1", Resource: string(WorkloadDeployments),
	},
	WorkloadStatefulSets: {
		Group: "apps", Version: "v1", Resource: string(WorkloadStatefulSets),
	},
	WorkloadDaemonSets: {
		Group: "apps", Version: "v1", Resource: string(WorkloadDaemonSets),
	},
	WorkloadJobs: {
		Group: "batch", Version: "v1", Resource: string(WorkloadJobs),
	},
	WorkloadCronJobs: {
		Group: "batch", Version: "v1", Resource: string(WorkloadCronJobs),
	},
}

func (service *Service) ListWorkloads(
	ctx context.Context,
	input ListWorkloadsInput,
) (WorkloadPage, error) {
	identity, exists := workloadIdentities[input.Resource]
	if !exists || len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return WorkloadPage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID:     input.ClusterID,
		Resource:      identity,
		Namespace:     input.Namespace,
		Limit:         input.Limit,
		ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return WorkloadPage{}, err
	}
	result := WorkloadPage{
		Workloads:          make([]WorkloadSummary, 0, len(page.Items)),
		ContinueToken:      page.ContinueToken,
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := workloadDetail(item, input.Resource, input.Namespace, "")
		if err != nil {
			return WorkloadPage{}, err
		}
		result.Workloads = append(result.Workloads, detail.WorkloadSummary)
	}
	return result, nil
}

func (service *Service) GetWorkload(
	ctx context.Context,
	clusterID string,
	namespace string,
	resource WorkloadResource,
	name string,
) (WorkloadDetail, error) {
	identity, exists := workloadIdentities[resource]
	if !exists ||
		len(k8svalidation.IsDNS1123Label(namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return WorkloadDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID,
		Resource:  identity,
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	return workloadDetail(object, resource, namespace, name)
}

func workloadDetail(
	object map[string]any,
	resource WorkloadResource,
	namespace string,
	name string,
) (WorkloadDetail, error) {
	unstructuredResource := &unstructured.Unstructured{Object: object}
	if unstructuredResource.GetNamespace() != namespace ||
		unstructuredResource.GetName() == "" ||
		(name != "" && unstructuredResource.GetName() != name) {
		return WorkloadDetail{}, ErrInvalidResponse
	}

	switch resource {
	case WorkloadDeployments:
		var workload appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload); err != nil ||
			workload.APIVersion != "apps/v1" ||
			workload.Kind != "Deployment" {
			return WorkloadDetail{}, ErrInvalidResponse
		}
		return deploymentDetail(&workload), nil
	case WorkloadStatefulSets:
		var workload appsv1.StatefulSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload); err != nil ||
			workload.APIVersion != "apps/v1" ||
			workload.Kind != "StatefulSet" {
			return WorkloadDetail{}, ErrInvalidResponse
		}
		return statefulSetDetail(&workload), nil
	case WorkloadDaemonSets:
		var workload appsv1.DaemonSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload); err != nil ||
			workload.APIVersion != "apps/v1" ||
			workload.Kind != "DaemonSet" {
			return WorkloadDetail{}, ErrInvalidResponse
		}
		return daemonSetDetail(&workload), nil
	case WorkloadJobs:
		var workload batchv1.Job
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload); err != nil ||
			workload.APIVersion != "batch/v1" ||
			workload.Kind != "Job" {
			return WorkloadDetail{}, ErrInvalidResponse
		}
		return jobDetail(&workload), nil
	case WorkloadCronJobs:
		var workload batchv1.CronJob
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload); err != nil ||
			workload.APIVersion != "batch/v1" ||
			workload.Kind != "CronJob" {
			return WorkloadDetail{}, ErrInvalidResponse
		}
		return cronJobDetail(&workload), nil
	default:
		return WorkloadDetail{}, ErrInvalidInput
	}
}

func deploymentDetail(workload *appsv1.Deployment) WorkloadDetail {
	replicas := desiredReplicas(workload.Spec.Replicas)
	summary := workloadSummary(
		WorkloadDeployments,
		workload.TypeMeta,
		workload.ObjectMeta,
		workload.Status.ObservedGeneration,
		deploymentStatus(workload, replicas),
		workload.Spec.Template.Spec,
	)
	summary.Replicas = &WorkloadReplicaStatus{
		Desired:     replicas,
		Current:     workload.Status.Replicas,
		Updated:     workload.Status.UpdatedReplicas,
		Ready:       workload.Status.ReadyReplicas,
		Available:   workload.Status.AvailableReplicas,
		Unavailable: workload.Status.UnavailableReplicas,
	}
	return workloadDetailFromPodTemplate(
		summary,
		workload.ObjectMeta,
		workload.Spec.Selector,
		workload.Spec.Template.Spec,
		deploymentConditions(workload.Status.Conditions),
		string(workload.Spec.Strategy.Type),
		workload.Spec.MinReadySeconds,
		workload.Spec.RevisionHistoryLimit,
	)
}

func statefulSetDetail(workload *appsv1.StatefulSet) WorkloadDetail {
	replicas := desiredReplicas(workload.Spec.Replicas)
	status := "progressing"
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.ReadyReplicas >= replicas &&
		workload.Status.CurrentRevision == workload.Status.UpdateRevision {
		status = "available"
	}
	summary := workloadSummary(
		WorkloadStatefulSets,
		workload.TypeMeta,
		workload.ObjectMeta,
		workload.Status.ObservedGeneration,
		status,
		workload.Spec.Template.Spec,
	)
	summary.Replicas = &WorkloadReplicaStatus{
		Desired:     replicas,
		Current:     workload.Status.CurrentReplicas,
		Updated:     workload.Status.UpdatedReplicas,
		Ready:       workload.Status.ReadyReplicas,
		Available:   workload.Status.AvailableReplicas,
		Unavailable: maxInt32(0, replicas-workload.Status.ReadyReplicas),
	}
	result := workloadDetailFromPodTemplate(
		summary,
		workload.ObjectMeta,
		workload.Spec.Selector,
		workload.Spec.Template.Spec,
		statefulSetConditions(workload.Status.Conditions),
		string(workload.Spec.UpdateStrategy.Type),
		workload.Spec.MinReadySeconds,
		workload.Spec.RevisionHistoryLimit,
	)
	result.ServiceName = workload.Spec.ServiceName
	return result
}

func daemonSetDetail(workload *appsv1.DaemonSet) WorkloadDetail {
	status := "progressing"
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.NumberAvailable >= workload.Status.DesiredNumberScheduled &&
		workload.Status.NumberUnavailable == 0 {
		status = "available"
	}
	summary := workloadSummary(
		WorkloadDaemonSets,
		workload.TypeMeta,
		workload.ObjectMeta,
		workload.Status.ObservedGeneration,
		status,
		workload.Spec.Template.Spec,
	)
	summary.Replicas = &WorkloadReplicaStatus{
		Desired:     workload.Status.DesiredNumberScheduled,
		Current:     workload.Status.CurrentNumberScheduled,
		Updated:     workload.Status.UpdatedNumberScheduled,
		Ready:       workload.Status.NumberReady,
		Available:   workload.Status.NumberAvailable,
		Unavailable: workload.Status.NumberUnavailable,
	}
	return workloadDetailFromPodTemplate(
		summary,
		workload.ObjectMeta,
		workload.Spec.Selector,
		workload.Spec.Template.Spec,
		daemonSetConditions(workload.Status.Conditions),
		string(workload.Spec.UpdateStrategy.Type),
		workload.Spec.MinReadySeconds,
		workload.Spec.RevisionHistoryLimit,
	)
}

func jobDetail(workload *batchv1.Job) WorkloadDetail {
	status := jobWorkloadStatus(workload)
	summary := workloadSummary(
		WorkloadJobs,
		workload.TypeMeta,
		workload.ObjectMeta,
		0,
		status,
		workload.Spec.Template.Spec,
	)
	summary.Job = &WorkloadJobStatus{
		Parallelism:    cloneInt32Pointer(workload.Spec.Parallelism),
		Completions:    cloneInt32Pointer(workload.Spec.Completions),
		Active:         workload.Status.Active,
		Succeeded:      workload.Status.Succeeded,
		Failed:         workload.Status.Failed,
		StartTime:      metav1TimePointer(workload.Status.StartTime),
		CompletionTime: metav1TimePointer(workload.Status.CompletionTime),
	}
	result := workloadDetailFromPodTemplate(
		summary,
		workload.ObjectMeta,
		workload.Spec.Selector,
		workload.Spec.Template.Spec,
		jobConditions(workload.Status.Conditions),
		"",
		0,
		nil,
	)
	if workload.Spec.CompletionMode != nil {
		result.CompletionMode = string(*workload.Spec.CompletionMode)
	}
	return result
}

func cronJobDetail(workload *batchv1.CronJob) WorkloadDetail {
	status := "scheduled"
	if workload.Spec.Suspend != nil && *workload.Spec.Suspend {
		status = "suspended"
	} else if len(workload.Status.Active) > 0 {
		status = "running"
	}
	summary := workloadSummary(
		WorkloadCronJobs,
		workload.TypeMeta,
		workload.ObjectMeta,
		0,
		status,
		workload.Spec.JobTemplate.Spec.Template.Spec,
	)
	summary.CronJob = &WorkloadCronJobStatus{
		Schedule:           workload.Spec.Schedule,
		Suspend:            workload.Spec.Suspend != nil && *workload.Spec.Suspend,
		Active:             len(workload.Status.Active),
		LastScheduleTime:   metav1TimePointer(workload.Status.LastScheduleTime),
		LastSuccessfulTime: metav1TimePointer(workload.Status.LastSuccessfulTime),
	}
	result := workloadDetailFromPodTemplate(
		summary,
		workload.ObjectMeta,
		workload.Spec.JobTemplate.Spec.Selector,
		workload.Spec.JobTemplate.Spec.Template.Spec,
		nil,
		"",
		0,
		nil,
	)
	result.ConcurrencyPolicy = string(workload.Spec.ConcurrencyPolicy)
	if workload.Spec.JobTemplate.Spec.CompletionMode != nil {
		result.CompletionMode = string(*workload.Spec.JobTemplate.Spec.CompletionMode)
	}
	return result
}

func workloadSummary(
	resource WorkloadResource,
	typeMeta metav1.TypeMeta,
	objectMeta metav1.ObjectMeta,
	observedGeneration int64,
	status string,
	podSpec corev1.PodSpec,
) WorkloadSummary {
	return WorkloadSummary{
		Resource:           resource,
		APIVersion:         typeMeta.APIVersion,
		Kind:               typeMeta.Kind,
		Namespace:          objectMeta.Namespace,
		Name:               objectMeta.Name,
		UID:                string(objectMeta.UID),
		ResourceVersion:    objectMeta.ResourceVersion,
		CreationTimestamp:  objectMeta.CreationTimestamp.Time,
		Generation:         objectMeta.Generation,
		ObservedGeneration: observedGeneration,
		Labels:             cloneMap(objectMeta.Labels),
		Status:             status,
		Images:             podImages(podSpec),
	}
}

func workloadDetailFromPodTemplate(
	summary WorkloadSummary,
	metadata metav1.ObjectMeta,
	selector *metav1.LabelSelector,
	podSpec corev1.PodSpec,
	conditions []WorkloadCondition,
	strategy string,
	minReadySeconds int32,
	revisionHistoryLimit *int32,
) WorkloadDetail {
	// The contract declares `conditions` as an array, and a nil slice marshals to
	// `null`, not `[]`. A CronJob has no conditions to report and passes nil, so
	// without this the one response that carries no conditions is the one that
	// breaks its own schema.
	if conditions == nil {
		conditions = []WorkloadCondition{}
	}
	return WorkloadDetail{
		WorkloadSummary:      summary,
		Annotations:          cloneMap(metadata.Annotations),
		Selector:             workloadSelector(selector),
		Containers:           workloadContainers(podSpec.Containers),
		InitContainers:       workloadContainers(podSpec.InitContainers),
		Conditions:           conditions,
		Strategy:             strategy,
		MinReadySeconds:      minReadySeconds,
		RevisionHistoryLimit: cloneInt32Pointer(revisionHistoryLimit),
	}
}

func workloadSelector(selector *metav1.LabelSelector) *WorkloadSelector {
	if selector == nil {
		return nil
	}
	result := &WorkloadSelector{
		MatchLabels:      cloneMap(selector.MatchLabels),
		MatchExpressions: make([]WorkloadSelectorRequirement, 0, len(selector.MatchExpressions)),
	}
	for _, expression := range selector.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadSelectorRequirement{
			Key:      expression.Key,
			Operator: string(expression.Operator),
			Values:   append([]string(nil), expression.Values...),
		})
	}
	return result
}

func workloadContainers(containers []corev1.Container) []WorkloadContainer {
	result := make([]WorkloadContainer, 0, len(containers))
	for _, container := range containers {
		result = append(result, WorkloadContainer{
			Name:            container.Name,
			Image:           container.Image,
			ImagePullPolicy: string(container.ImagePullPolicy),
		})
	}
	return result
}

func podImages(spec corev1.PodSpec) []string {
	unique := make(map[string]struct{}, len(spec.Containers)+len(spec.InitContainers))
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, container := range containers {
			if container.Image != "" {
				unique[container.Image] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(unique))
	for image := range unique {
		result = append(result, image)
	}
	sort.Strings(result)
	return result
}

func deploymentStatus(workload *appsv1.Deployment, desired int32) string {
	if workload.Spec.Paused {
		return "suspended"
	}
	for _, condition := range workload.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return "degraded"
		}
	}
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.AvailableReplicas >= desired &&
		workload.Status.UnavailableReplicas == 0 {
		return "available"
	}
	return "progressing"
}

func jobWorkloadStatus(workload *batchv1.Job) string {
	if workload.Spec.Suspend != nil && *workload.Spec.Suspend {
		return "suspended"
	}
	for _, condition := range workload.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobFailed:
			return "failed"
		case batchv1.JobComplete:
			return "completed"
		}
	}
	if workload.Status.Active > 0 {
		return "running"
	}
	return "pending"
}

func deploymentConditions(conditions []appsv1.DeploymentCondition) []WorkloadCondition {
	result := make([]WorkloadCondition, 0, len(conditions))
	for _, condition := range conditions {
		result = append(result, WorkloadCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return result
}

func statefulSetConditions(conditions []appsv1.StatefulSetCondition) []WorkloadCondition {
	result := make([]WorkloadCondition, 0, len(conditions))
	for _, condition := range conditions {
		result = append(result, WorkloadCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return result
}

func daemonSetConditions(conditions []appsv1.DaemonSetCondition) []WorkloadCondition {
	result := make([]WorkloadCondition, 0, len(conditions))
	for _, condition := range conditions {
		result = append(result, WorkloadCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return result
}

func jobConditions(conditions []batchv1.JobCondition) []WorkloadCondition {
	result := make([]WorkloadCondition, 0, len(conditions))
	for _, condition := range conditions {
		result = append(result, WorkloadCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return result
}

func desiredReplicas(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}

func cloneInt32Pointer(value *int32) *int32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func metav1TimePointer(value *metav1.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.Time
	return &result
}

func maxInt32(left, right int32) int32 {
	if left > right {
		return left
	}
	return right
}
