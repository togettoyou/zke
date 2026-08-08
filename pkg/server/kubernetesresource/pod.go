package kubernetesresource

import (
	"context"
	"maps"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var podIdentity = ResourceIdentity{
	Version:  "v1",
	Resource: "pods",
}

type ListPodsInput struct {
	ClusterID     string
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type PodPage struct {
	Pods               []PodSummary `json:"pods"`
	ContinueToken      string       `json:"continue_token"`
	ResourceVersion    string       `json:"resource_version"`
	RemainingItemCount *int64       `json:"remaining_item_count"`
}

type PodSummary struct {
	APIVersion        string             `json:"api_version"`
	Kind              string             `json:"kind"`
	Namespace         string             `json:"namespace"`
	Name              string             `json:"name"`
	UID               string             `json:"uid"`
	ResourceVersion   string             `json:"resource_version"`
	CreationTimestamp time.Time          `json:"creation_timestamp"`
	DeletionTimestamp *time.Time         `json:"deletion_timestamp,omitempty"`
	Labels            map[string]string  `json:"labels"`
	Phase             string             `json:"phase"`
	Reason            string             `json:"reason"`
	Ready             bool               `json:"ready"`
	NodeName          string             `json:"node_name"`
	PodIP             string             `json:"pod_ip"`
	RestartCount      int64              `json:"restart_count"`
	Images            []string           `json:"images"`
	Controller        *PodOwnerReference `json:"controller,omitempty"`
}

type PodDetail struct {
	PodSummary
	Annotations         map[string]string   `json:"annotations"`
	OwnerReferences     []PodOwnerReference `json:"owner_references"`
	Message             string              `json:"message"`
	NominatedNodeName   string              `json:"nominated_node_name"`
	ServiceAccountName  string              `json:"service_account_name"`
	SchedulerName       string              `json:"scheduler_name"`
	PriorityClassName   string              `json:"priority_class_name"`
	RuntimeClassName    string              `json:"runtime_class_name"`
	RestartPolicy       string              `json:"restart_policy"`
	DNSPolicy           string              `json:"dns_policy"`
	HostNetwork         bool                `json:"host_network"`
	HostIPs             []string            `json:"host_ips"`
	PodIPs              []string            `json:"pod_ips"`
	StartTime           *time.Time          `json:"start_time,omitempty"`
	QOSClass            string              `json:"qos_class"`
	Containers          []PodContainer      `json:"containers"`
	InitContainers      []PodContainer      `json:"init_containers"`
	EphemeralContainers []PodContainer      `json:"ephemeral_containers"`
	Conditions          []PodCondition      `json:"conditions"`
}

type PodOwnerReference struct {
	APIVersion         string `json:"api_version"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	UID                string `json:"uid"`
	Controller         bool   `json:"controller"`
	BlockOwnerDeletion bool   `json:"block_owner_deletion"`
}

type PodContainer struct {
	Name            string             `json:"name"`
	Image           string             `json:"image"`
	ImagePullPolicy string             `json:"image_pull_policy"`
	Requests        map[string]string  `json:"requests"`
	Limits          map[string]string  `json:"limits"`
	Ready           bool               `json:"ready"`
	Started         *bool              `json:"started,omitempty"`
	RestartCount    int32              `json:"restart_count"`
	State           PodContainerState  `json:"state"`
	LastState       *PodContainerState `json:"last_state,omitempty"`
}

type PodContainerState struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	// What the kubelet said about this state. The reason names the class of
	// failure and the message names the instance of it: `CreateContainerConfigError`
	// is the same reason whether the missing thing is a Secret or a key inside
	// one, and only the message says which.
	Message    string     `json:"message"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int32     `json:"exit_code,omitempty"`
	Signal     *int32     `json:"signal,omitempty"`
}

type PodCondition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	ObservedGeneration int64      `json:"observed_generation"`
	Reason             string     `json:"reason"`
	Message            string     `json:"message"`
	LastProbeTime      *time.Time `json:"last_probe_time,omitempty"`
	LastTransitionTime *time.Time `json:"last_transition_time,omitempty"`
}

type DeletePodInput struct {
	ClusterID          string
	Namespace          string
	Name               string
	UID                string
	ResourceVersion    string
	GracePeriodSeconds *int64
	Propagation        agentv1.DeletePropagation
	DryRun             bool
	Confirm            bool
	IdempotencyKey     string
}

func (service *Service) ListPods(
	ctx context.Context,
	input ListPodsInput,
) (PodPage, error) {
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID:     input.ClusterID,
		Resource:      podIdentity,
		Namespace:     input.Namespace,
		Limit:         input.Limit,
		ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return PodPage{}, err
	}
	result := PodPage{
		Pods:               make([]PodSummary, 0, len(page.Items)),
		ContinueToken:      page.ContinueToken,
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := podDetail(item, input.Namespace, "")
		if err != nil {
			return PodPage{}, err
		}
		result.Pods = append(result.Pods, detail.PodSummary)
	}
	return result, nil
}

func (service *Service) GetPod(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
) (PodDetail, error) {
	if len(k8svalidation.IsDNS1123Label(namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return PodDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID,
		Resource:  podIdentity,
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return PodDetail{}, err
	}
	return podDetail(object, namespace, name)
}

func (service *Service) DeletePod(ctx context.Context, input DeletePodInput) error {
	if len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.Name)) != 0 ||
		strings.TrimSpace(input.UID) == "" {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID:          input.ClusterID,
		Resource:           podIdentity,
		Namespace:          input.Namespace,
		Name:               input.Name,
		DryRun:             input.DryRun,
		Confirm:            input.Confirm,
		GracePeriodSeconds: input.GracePeriodSeconds,
		Propagation:        input.Propagation,
		Preconditions: DeletePreconditions{
			UID:             input.UID,
			ResourceVersion: input.ResourceVersion,
		},
		IdempotencyKey: input.IdempotencyKey,
	})
}

func podDetail(object map[string]any, namespace string, name string) (PodDetail, error) {
	resource := &unstructured.Unstructured{Object: object}
	if resource.GetNamespace() != namespace ||
		resource.GetName() == "" ||
		(name != "" && resource.GetName() != name) {
		return PodDetail{}, ErrInvalidResponse
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &pod); err != nil ||
		pod.APIVersion != "v1" ||
		pod.Kind != "Pod" {
		return PodDetail{}, ErrInvalidResponse
	}

	labels := maps.Clone(pod.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := maps.Clone(pod.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	owners := podOwnerReferences(pod.OwnerReferences)
	conditions := podConditions(pod.Status.Conditions)
	containerStatuses := containerStatusByName(pod.Status.ContainerStatuses)
	initContainerStatuses := containerStatusByName(pod.Status.InitContainerStatuses)
	ephemeralContainerStatuses := containerStatusByName(pod.Status.EphemeralContainerStatuses)

	return PodDetail{
		PodSummary: PodSummary{
			APIVersion:        pod.APIVersion,
			Kind:              pod.Kind,
			Namespace:         pod.Namespace,
			Name:              pod.Name,
			UID:               string(pod.UID),
			ResourceVersion:   pod.ResourceVersion,
			CreationTimestamp: pod.CreationTimestamp.Time,
			DeletionTimestamp: metav1TimePointer(pod.DeletionTimestamp),
			Labels:            labels,
			Phase:             string(pod.Status.Phase),
			Reason:            pod.Status.Reason,
			Ready:             podIsReady(pod.Status.Conditions),
			NodeName:          pod.Spec.NodeName,
			PodIP:             pod.Status.PodIP,
			RestartCount:      podRestartCount(pod.Status),
			Images:            podAllImages(pod.Spec),
			Controller:        podController(owners),
		},
		Annotations:        annotations,
		OwnerReferences:    owners,
		Message:            pod.Status.Message,
		NominatedNodeName:  pod.Status.NominatedNodeName,
		ServiceAccountName: pod.Spec.ServiceAccountName,
		SchedulerName:      pod.Spec.SchedulerName,
		PriorityClassName:  pod.Spec.PriorityClassName,
		RuntimeClassName:   stringValue(pod.Spec.RuntimeClassName),
		RestartPolicy:      string(pod.Spec.RestartPolicy),
		DNSPolicy:          string(pod.Spec.DNSPolicy),
		HostNetwork:        pod.Spec.HostNetwork,
		HostIPs:            podHostIPs(pod.Status),
		PodIPs:             podPodIPs(pod.Status),
		StartTime:          metav1TimePointer(pod.Status.StartTime),
		QOSClass:           string(pod.Status.QOSClass),
		Containers:         podContainers(pod.Spec.Containers, containerStatuses),
		InitContainers:     podContainers(pod.Spec.InitContainers, initContainerStatuses),
		EphemeralContainers: podEphemeralContainers(
			pod.Spec.EphemeralContainers,
			ephemeralContainerStatuses,
		),
		Conditions: conditions,
	}, nil
}

func podOwnerReferences(input []metav1.OwnerReference) []PodOwnerReference {
	result := make([]PodOwnerReference, 0, len(input))
	for _, owner := range input {
		result = append(result, PodOwnerReference{
			APIVersion:         owner.APIVersion,
			Kind:               owner.Kind,
			Name:               owner.Name,
			UID:                string(owner.UID),
			Controller:         boolValue(owner.Controller),
			BlockOwnerDeletion: boolValue(owner.BlockOwnerDeletion),
		})
	}
	return result
}

func podController(owners []PodOwnerReference) *PodOwnerReference {
	for _, owner := range owners {
		if owner.Controller {
			result := owner
			return &result
		}
	}
	return nil
}

func podIsReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podRestartCount(status corev1.PodStatus) int64 {
	var result int64
	for _, container := range status.InitContainerStatuses {
		result += int64(container.RestartCount)
	}
	for _, container := range status.ContainerStatuses {
		result += int64(container.RestartCount)
	}
	for _, container := range status.EphemeralContainerStatuses {
		result += int64(container.RestartCount)
	}
	return result
}

func containerStatusByName(input []corev1.ContainerStatus) map[string]corev1.ContainerStatus {
	result := make(map[string]corev1.ContainerStatus, len(input))
	for _, status := range input {
		result[status.Name] = status
	}
	return result
}

func podContainers(
	input []corev1.Container,
	statuses map[string]corev1.ContainerStatus,
) []PodContainer {
	result := make([]PodContainer, 0, len(input))
	for _, container := range input {
		result = append(result, podContainer(
			container.Name,
			container.Image,
			container.ImagePullPolicy,
			container.Resources,
			statuses[container.Name],
		))
	}
	return result
}

func podEphemeralContainers(
	input []corev1.EphemeralContainer,
	statuses map[string]corev1.ContainerStatus,
) []PodContainer {
	result := make([]PodContainer, 0, len(input))
	for _, container := range input {
		result = append(result, podContainer(
			container.Name,
			container.Image,
			container.ImagePullPolicy,
			container.Resources,
			statuses[container.Name],
		))
	}
	return result
}

func podContainer(
	name string,
	image string,
	pullPolicy corev1.PullPolicy,
	resources corev1.ResourceRequirements,
	status corev1.ContainerStatus,
) PodContainer {
	state := PodContainerState{Type: "unknown"}
	var lastState *PodContainerState
	if status.Name != "" {
		state = podContainerState(status.State)
		if value, present := optionalPodContainerState(status.LastTerminationState); present {
			lastState = &value
		}
	}
	return PodContainer{
		Name:            name,
		Image:           image,
		ImagePullPolicy: string(pullPolicy),
		Requests:        resourceQuantities(resources.Requests),
		Limits:          resourceQuantities(resources.Limits),
		Ready:           status.Ready,
		Started:         copyPointer(status.Started),
		RestartCount:    status.RestartCount,
		State:           state,
		LastState:       lastState,
	}
}

func podAllImages(spec corev1.PodSpec) []string {
	result := podImages(spec)
	for _, container := range spec.EphemeralContainers {
		result = append(result, container.Image)
	}
	return result
}

func podContainerState(input corev1.ContainerState) PodContainerState {
	if input.Running != nil {
		return PodContainerState{
			Type:      "running",
			StartedAt: timePointer(input.Running.StartedAt),
		}
	}
	if input.Terminated != nil {
		exitCode := input.Terminated.ExitCode
		signal := input.Terminated.Signal
		return PodContainerState{
			Type:       "terminated",
			Reason:     input.Terminated.Reason,
			Message:    input.Terminated.Message,
			StartedAt:  timePointer(input.Terminated.StartedAt),
			FinishedAt: timePointer(input.Terminated.FinishedAt),
			ExitCode:   &exitCode,
			Signal:     &signal,
		}
	}
	if input.Waiting != nil {
		return PodContainerState{
			Type:    "waiting",
			Reason:  input.Waiting.Reason,
			Message: input.Waiting.Message,
		}
	}
	return PodContainerState{Type: "unknown"}
}

func optionalPodContainerState(input corev1.ContainerState) (PodContainerState, bool) {
	if input.Running == nil && input.Terminated == nil && input.Waiting == nil {
		return PodContainerState{}, false
	}
	return podContainerState(input), true
}

func podConditions(input []corev1.PodCondition) []PodCondition {
	result := make([]PodCondition, 0, len(input))
	for _, condition := range input {
		result = append(result, PodCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			ObservedGeneration: condition.ObservedGeneration,
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastProbeTime:      timePointer(condition.LastProbeTime),
			LastTransitionTime: timePointer(condition.LastTransitionTime),
		})
	}
	return result
}

func resourceQuantities(input corev1.ResourceList) map[string]string {
	result := make(map[string]string, len(input))
	for name, quantity := range input {
		result[string(name)] = quantity.String()
	}
	return result
}

func podHostIPs(status corev1.PodStatus) []string {
	result := make([]string, 0, len(status.HostIPs))
	for _, address := range status.HostIPs {
		result = append(result, address.IP)
	}
	if len(result) == 0 && status.HostIP != "" {
		result = append(result, status.HostIP)
	}
	return result
}

func podPodIPs(status corev1.PodStatus) []string {
	result := make([]string, 0, len(status.PodIPs))
	for _, address := range status.PodIPs {
		result = append(result, address.IP)
	}
	if len(result) == 0 && status.PodIP != "" {
		result = append(result, status.PodIP)
	}
	return result
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timePointer(value metav1.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	result := value.Time
	return &result
}
