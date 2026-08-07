package kubernetesresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"strings"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	workloadSelectorLabel   = "zke.io/workload-id"
	maxWorkloadContainers   = 100
	maxWorkloadImageBytes   = 2048
	maxCronJobScheduleBytes = 512
	maxCronJobTimeZoneBytes = 128
	// A Job's name is copied onto its Pods as the `job-name` label value, and a
	// CronJob's has to leave room for the Job names the controller derives.
	maxJobNameLength     = 63
	maxCronJobNameLength = 52
)

// One container of the typed Pod template.
//
// The same shape in both directions: a detail response returns the containers
// in these fields, and an update submits them back. A form that reads one shape
// and writes another has to map between them twice, and every field missing
// from the read side silently becomes a field the form deletes on save.
type WorkloadContainerTemplate struct {
	Name            string                        `json:"name"`
	Image           string                        `json:"image"`
	ImagePullPolicy string                        `json:"image_pull_policy,omitempty"`
	Command         []string                      `json:"command,omitempty"`
	Args            []string                      `json:"args,omitempty"`
	WorkingDir      string                        `json:"working_dir,omitempty"`
	Env             []WorkloadEnvVar              `json:"env,omitempty"`
	Resources       *WorkloadResourceRequirements `json:"resources,omitempty"`
	VolumeMounts    []WorkloadVolumeMount         `json:"volume_mounts,omitempty"`
	LivenessProbe   *WorkloadProbe                `json:"liveness_probe,omitempty"`
	ReadinessProbe  *WorkloadProbe                `json:"readiness_probe,omitempty"`
	Lifecycle       *WorkloadLifecycle            `json:"lifecycle,omitempty"`
	Privileged      *bool                         `json:"privileged,omitempty"`
}

// The part of a workload this platform models, submitted the same way whether
// the object is being created or edited.
//
// One definition rather than two because an edit form is the create form on an
// existing object: a field that only one of them accepted would be a field an
// operator can set once and never correct, or correct but never set.
type WorkloadSpecInput struct {
	Labels         map[string]string
	Annotations    map[string]string
	Description    string
	Containers     []WorkloadContainerTemplate
	InitContainers []WorkloadContainerTemplate

	Volumes          []WorkloadVolume
	ImagePullSecrets []string
	NodeSelector     map[string]string
	Tolerations      []WorkloadToleration

	Replicas    *int32
	ServiceName string

	Parallelism             *int32
	Completions             *int32
	BackoffLimit            *int32
	TTLSecondsAfterFinished *int32

	Schedule                   string
	TimeZone                   string
	Suspend                    *bool
	ConcurrencyPolicy          string
	StartingDeadlineSeconds    *int64
	SuccessfulJobsHistoryLimit *int32
	FailedJobsHistoryLimit     *int32
}

type CreateWorkloadInput struct {
	ClusterID string
	Namespace string
	Resource  WorkloadResource
	Name      string
	WorkloadSpecInput

	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

func (service *Service) CreateWorkload(
	ctx context.Context,
	input CreateWorkloadInput,
) (WorkloadDetail, error) {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists || validateCreateWorkloadInput(input) != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	object, err := createWorkloadObject(input)
	if err != nil {
		return WorkloadDetail{}, err
	}
	unstructuredObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Object:    unstructuredObject,
		Options: MutationOptions{
			DryRun: input.DryRun,
		},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	return workloadDetail(result, input.Resource, input.Namespace, input.Name)
}

func validateCreateWorkloadInput(input CreateWorkloadInput) error {
	if !validWorkloadTarget(input.Namespace, input.Name, input.Resource) ||
		!validWorkloadSpecFields(input.WorkloadSpecInput) {
		return ErrInvalidInput
	}

	spec := input.WorkloadSpecInput
	switch input.Resource {
	case WorkloadDeployments:
		if hasStatefulSetFields(spec) ||
			hasJobFields(spec) ||
			hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadStatefulSets:
		// Required at creation and immutable afterwards, which is why the update
		// path refuses it rather than sharing this branch.
		if len(k8svalidation.IsDNS1035Label(spec.ServiceName)) != 0 ||
			hasJobFields(spec) ||
			hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadDaemonSets:
		if spec.Replicas != nil ||
			hasStatefulSetFields(spec) ||
			hasJobFields(spec) ||
			hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadJobs:
		if spec.Replicas != nil ||
			hasStatefulSetFields(spec) ||
			hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadCronJobs:
		if spec.Replicas != nil ||
			hasStatefulSetFields(spec) ||
			!validCronJobFields(spec) {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

// Namespace, name and the name length the type imposes on it.
func validWorkloadTarget(namespace string, name string, resource WorkloadResource) bool {
	if len(k8svalidation.IsDNS1123Label(namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return false
	}
	switch resource {
	case WorkloadJobs:
		return len(name) <= maxJobNameLength
	case WorkloadCronJobs:
		return len(name) <= maxCronJobNameLength
	default:
		return true
	}
}

// Everything a create and an update validate the same way.
func validWorkloadSpecFields(spec WorkloadSpecInput) bool {
	_, hasReservedSelectorLabel := spec.Labels[workloadSelectorLabel]
	return validNamespaceLabels(spec.Labels) &&
		!hasReservedSelectorLabel &&
		validWorkloadAnnotations(spec.Annotations) &&
		utf8.RuneCountInString(spec.Description) <= maxWorkloadDescriptionRunes &&
		validWorkloadContainers(spec.Containers, spec.InitContainers) &&
		validWorkloadVolumes(
			spec.Volumes,
			workloadMountedVolumeNames(spec.Containers, spec.InitContainers),
		) &&
		validWorkloadImagePullSecrets(spec.ImagePullSecrets) &&
		validWorkloadNodeSelector(spec.NodeSelector) &&
		validWorkloadTolerations(spec.Tolerations) &&
		validNonNegativeInt32(spec.Replicas) &&
		validNonNegativeInt32(spec.Parallelism) &&
		validNonNegativeInt32(spec.Completions) &&
		validNonNegativeInt32(spec.BackoffLimit) &&
		validNonNegativeInt32(spec.TTLSecondsAfterFinished) &&
		validNonNegativeInt64(spec.StartingDeadlineSeconds) &&
		validNonNegativeInt32(spec.SuccessfulJobsHistoryLimit) &&
		validNonNegativeInt32(spec.FailedJobsHistoryLimit)
}

func validWorkloadContainers(
	containers []WorkloadContainerTemplate,
	initContainers []WorkloadContainerTemplate,
) bool {
	if len(containers) == 0 ||
		len(containers) > maxWorkloadContainers ||
		len(initContainers) > maxWorkloadContainers {
		return false
	}
	names := make(map[string]struct{}, len(containers)+len(initContainers))
	addContainer := func(container WorkloadContainerTemplate) bool {
		if len(k8svalidation.IsDNS1123Label(container.Name)) != 0 ||
			container.Image == "" ||
			len(container.Image) > maxWorkloadImageBytes ||
			strings.TrimSpace(container.Image) != container.Image ||
			strings.ContainsAny(container.Image, " \t\r\n") ||
			!validImagePullPolicy(container.ImagePullPolicy) ||
			!validWorkloadContainerTemplate(container) {
			return false
		}
		if _, exists := names[container.Name]; exists {
			return false
		}
		names[container.Name] = struct{}{}
		return true
	}
	for _, container := range containers {
		if !addContainer(container) {
			return false
		}
	}
	for _, container := range initContainers {
		// An init container runs to completion before the Pod starts: it has
		// nothing to probe and no lifecycle to hook into, and Kubernetes rejects
		// both rather than ignoring them.
		if container.LivenessProbe != nil ||
			container.ReadinessProbe != nil ||
			container.Lifecycle != nil ||
			!addContainer(container) {
			return false
		}
	}
	return true
}

func validImagePullPolicy(value string) bool {
	switch corev1.PullPolicy(value) {
	case "", corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		return true
	default:
		return false
	}
}

func validNonNegativeInt32(value *int32) bool {
	return value == nil || *value >= 0
}

func validNonNegativeInt64(value *int64) bool {
	return value == nil || *value >= 0
}

func hasStatefulSetFields(spec WorkloadSpecInput) bool {
	return spec.ServiceName != ""
}

func hasJobFields(spec WorkloadSpecInput) bool {
	return spec.Parallelism != nil ||
		spec.Completions != nil ||
		spec.BackoffLimit != nil ||
		spec.TTLSecondsAfterFinished != nil
}

func hasCronJobFields(spec WorkloadSpecInput) bool {
	return spec.Schedule != "" ||
		spec.TimeZone != "" ||
		spec.Suspend != nil ||
		spec.ConcurrencyPolicy != "" ||
		spec.StartingDeadlineSeconds != nil ||
		spec.SuccessfulJobsHistoryLimit != nil ||
		spec.FailedJobsHistoryLimit != nil
}

func validCronJobFields(spec WorkloadSpecInput) bool {
	if spec.Schedule == "" ||
		len(spec.Schedule) > maxCronJobScheduleBytes ||
		strings.TrimSpace(spec.Schedule) != spec.Schedule ||
		len(spec.TimeZone) > maxCronJobTimeZoneBytes ||
		strings.TrimSpace(spec.TimeZone) != spec.TimeZone {
		return false
	}
	switch batchv1.ConcurrencyPolicy(spec.ConcurrencyPolicy) {
	case "", batchv1.AllowConcurrent, batchv1.ForbidConcurrent, batchv1.ReplaceConcurrent:
		return true
	default:
		return false
	}
}

func createWorkloadObject(input CreateWorkloadInput) (runtime.Object, error) {
	metadata := metav1.ObjectMeta{
		Name:        input.Name,
		Namespace:   input.Namespace,
		Labels:      maps.Clone(input.Labels),
		Annotations: workloadAnnotations(input.Annotations, input.Description),
	}
	selectorLabels := map[string]string{
		workloadSelectorLabel: workloadSelectorValue(input.Resource, input.Name),
	}
	controllerTemplate := workloadPodTemplate(
		input,
		selectorLabels,
		corev1.RestartPolicyAlways,
	)
	jobTemplate := workloadPodTemplate(
		input,
		selectorLabels,
		corev1.RestartPolicyNever,
	)

	switch input.Resource {
	case WorkloadDeployments:
		return &appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metadata,
			Spec: appsv1.DeploymentSpec{
				Replicas: copyPointer(input.Replicas),
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
				Template: controllerTemplate,
			},
		}, nil
	case WorkloadStatefulSets:
		return &appsv1.StatefulSet{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
			ObjectMeta: metadata,
			Spec: appsv1.StatefulSetSpec{
				Replicas:    copyPointer(input.Replicas),
				ServiceName: input.ServiceName,
				Selector:    &metav1.LabelSelector{MatchLabels: selectorLabels},
				Template:    controllerTemplate,
			},
		}, nil
	case WorkloadDaemonSets:
		return &appsv1.DaemonSet{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
			ObjectMeta: metadata,
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
				Template: controllerTemplate,
			},
		}, nil
	case WorkloadJobs:
		return &batchv1.Job{
			TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
			ObjectMeta: metadata,
			Spec:       workloadJobSpec(input, jobTemplate),
		}, nil
	case WorkloadCronJobs:
		return &batchv1.CronJob{
			TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
			ObjectMeta: metadata,
			Spec: batchv1.CronJobSpec{
				Schedule:                   input.Schedule,
				TimeZone:                   stringPointer(input.TimeZone),
				StartingDeadlineSeconds:    copyPointer(input.StartingDeadlineSeconds),
				ConcurrencyPolicy:          batchv1.ConcurrencyPolicy(input.ConcurrencyPolicy),
				Suspend:                    copyPointer(input.Suspend),
				SuccessfulJobsHistoryLimit: copyPointer(input.SuccessfulJobsHistoryLimit),
				FailedJobsHistoryLimit:     copyPointer(input.FailedJobsHistoryLimit),
				JobTemplate: batchv1.JobTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(input.Labels)},
					Spec:       workloadJobSpec(input, jobTemplate),
				},
			},
		}, nil
	default:
		return nil, ErrInvalidInput
	}
}

func workloadPodTemplate(
	input CreateWorkloadInput,
	selectorLabels map[string]string,
	restartPolicy corev1.RestartPolicy,
) corev1.PodTemplateSpec {
	labels := maps.Clone(input.Labels)
	if labels == nil {
		labels = make(map[string]string, len(selectorLabels))
	}
	for key, value := range selectorLabels {
		labels[key] = value
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			// The Pod carries the workload's annotations too: a description or an
			// operator's own key is about the thing that runs, and reading it off
			// a Pod is how it gets found again.
			Annotations: workloadAnnotations(input.Annotations, input.Description),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:    restartPolicy,
			Containers:       protocolWorkloadContainers(input.Containers),
			InitContainers:   protocolWorkloadContainers(input.InitContainers),
			Volumes:          workloadVolumeSpec(input.Volumes),
			ImagePullSecrets: workloadImagePullSecretSpec(input.ImagePullSecrets),
			NodeSelector:     maps.Clone(input.NodeSelector),
			Tolerations:      workloadTolerationSpec(input.Tolerations),
		},
	}
}

func protocolWorkloadContainers(input []WorkloadContainerTemplate) []corev1.Container {
	result := make([]corev1.Container, 0, len(input))
	for _, container := range input {
		result = append(result, workloadContainerSpec(container))
	}
	return result
}

func workloadJobSpec(
	input CreateWorkloadInput,
	template corev1.PodTemplateSpec,
) batchv1.JobSpec {
	return batchv1.JobSpec{
		Parallelism:             copyPointer(input.Parallelism),
		Completions:             copyPointer(input.Completions),
		BackoffLimit:            copyPointer(input.BackoffLimit),
		TTLSecondsAfterFinished: copyPointer(input.TTLSecondsAfterFinished),
		Template:                template,
	}
}

func workloadSelectorValue(resource WorkloadResource, name string) string {
	digest := sha256.Sum256([]byte(string(resource) + "\x00" + name))
	return hex.EncodeToString(digest[:16])
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func copyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
