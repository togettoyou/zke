package kubernetesresource

import (
	"context"
	"maps"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// UpdateWorkloadInput is a typed update of one workload.
//
// The modeled fields replace what is on the object; everything else is kept.
// That is the whole design, and it is not the same rule the smaller typed
// resources use. A Service or a NetworkPolicy is read as one piece and so is
// replaced as one piece, but a Pod template carries service accounts, host
// networking, host ports and the rest of the security context — fields this
// form does not model and which some operator set deliberately. Replacing the spec wholesale would delete them on
// a save that only meant to change an image tag.
//
// So the update reads the object back under its UID and resourceVersion, writes
// the modeled fields onto it, and leaves the rest of the document untouched.
type UpdateWorkloadInput struct {
	ClusterID string
	Namespace string
	Resource  WorkloadResource
	Name      string
	// The version the form was filled in against. Both must still match.
	UID             string
	ResourceVersion string
	WorkloadSpecInput

	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

func (service *Service) UpdateWorkload(
	ctx context.Context,
	input UpdateWorkloadInput,
) (WorkloadDetail, error) {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists || validateUpdateWorkloadInput(input) != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Name:      input.Name,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID ||
		current.GetResourceVersion() != input.ResourceVersion {
		return WorkloadDetail{}, ErrUpstreamConflict
	}
	updated, err := updateWorkloadObject(existing, input)
	if err != nil {
		return WorkloadDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Name:      input.Name,
		Object:    updated,
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

// What may be submitted, per type.
//
// The differences from a create are Kubernetes' own immutability rules rather
// than choices made here: a StatefulSet's governing Service and a Job's whole
// template are fixed at creation, and an API Server that is going to reject the
// write is better refused before a cluster round trip than after one.
func validateUpdateWorkloadInput(input UpdateWorkloadInput) error {
	if !validWorkloadTarget(input.Namespace, input.Name, input.Resource) ||
		!validWorkloadMutationIdentity(input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}

	spec := input.WorkloadSpecInput
	if input.Resource == WorkloadJobs {
		// A Job's Pod template, selector, completions and backoff limit are all
		// immutable. Only these two may move, so the rest must be absent rather
		// than silently ignored: a form that submitted a changed image and got a
		// success back would have written nothing.
		if len(spec.Containers) != 0 ||
			len(spec.InitContainers) != 0 ||
			len(spec.Volumes) != 0 ||
			len(spec.Labels) != 0 ||
			len(spec.Annotations) != 0 ||
			spec.Description != "" ||
			len(spec.ImagePullSecrets) != 0 ||
			len(spec.NodeSelector) != 0 ||
			len(spec.Tolerations) != 0 ||
			spec.Affinity != nil ||
			spec.TopologySpreadConstraints != nil ||
			spec.Replicas != nil ||
			hasStatefulSetFields(spec) ||
			hasCronJobFields(spec) ||
			spec.Completions != nil ||
			spec.BackoffLimit != nil {
			return ErrInvalidInput
		}
		if !validNonNegativeInt32(spec.Parallelism) ||
			!validNonNegativeInt32(spec.TTLSecondsAfterFinished) {
			return ErrInvalidInput
		}
		return nil
	}

	if !validWorkloadSpecFields(spec) {
		return ErrInvalidInput
	}
	switch input.Resource {
	case WorkloadDeployments:
		if hasStatefulSetFields(spec) || hasJobFields(spec) || hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadStatefulSets:
		// `serviceName` is rejected rather than accepted-and-ignored: it cannot
		// change, and accepting it would let a form believe it had.
		if hasStatefulSetFields(spec) || hasJobFields(spec) || hasCronJobFields(spec) {
			return ErrInvalidInput
		}
	case WorkloadDaemonSets:
		if spec.Replicas != nil ||
			hasStatefulSetFields(spec) ||
			hasJobFields(spec) ||
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

func validWorkloadMutationIdentity(uid string, resourceVersion string) bool {
	return uid != "" && resourceVersion != ""
}

func updateWorkloadObject(
	existing map[string]any,
	input UpdateWorkloadInput,
) (map[string]any, error) {
	spec := input.WorkloadSpecInput
	switch input.Resource {
	case WorkloadDeployments:
		var object appsv1.Deployment
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		applyWorkloadMetadata(&object.ObjectMeta, spec)
		applyWorkloadPodTemplate(&object.Spec.Template, object.Spec.Selector, spec)
		if spec.Replicas != nil {
			object.Spec.Replicas = copyPointer(spec.Replicas)
		}
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case WorkloadStatefulSets:
		var object appsv1.StatefulSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		applyWorkloadMetadata(&object.ObjectMeta, spec)
		applyWorkloadPodTemplate(&object.Spec.Template, object.Spec.Selector, spec)
		if spec.Replicas != nil {
			object.Spec.Replicas = copyPointer(spec.Replicas)
		}
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case WorkloadDaemonSets:
		var object appsv1.DaemonSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		applyWorkloadMetadata(&object.ObjectMeta, spec)
		applyWorkloadPodTemplate(&object.Spec.Template, object.Spec.Selector, spec)
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case WorkloadJobs:
		var object batchv1.Job
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		if spec.Parallelism != nil {
			object.Spec.Parallelism = copyPointer(spec.Parallelism)
		}
		if spec.TTLSecondsAfterFinished != nil {
			object.Spec.TTLSecondsAfterFinished = copyPointer(spec.TTLSecondsAfterFinished)
		}
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case WorkloadCronJobs:
		var object batchv1.CronJob
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		applyWorkloadMetadata(&object.ObjectMeta, spec)
		applyWorkloadPodTemplate(
			&object.Spec.JobTemplate.Spec.Template,
			object.Spec.JobTemplate.Spec.Selector,
			spec,
		)
		// The Job template's own labels, kept in step with the workload's the way
		// a create writes them.
		object.Spec.JobTemplate.Labels = maps.Clone(spec.Labels)
		object.Spec.Schedule = spec.Schedule
		object.Spec.TimeZone = stringPointer(spec.TimeZone)
		object.Spec.ConcurrencyPolicy = batchv1.ConcurrencyPolicy(spec.ConcurrencyPolicy)
		object.Spec.StartingDeadlineSeconds = copyPointer(spec.StartingDeadlineSeconds)
		object.Spec.SuccessfulJobsHistoryLimit = copyPointer(spec.SuccessfulJobsHistoryLimit)
		object.Spec.FailedJobsHistoryLimit = copyPointer(spec.FailedJobsHistoryLimit)
		if spec.Suspend != nil {
			object.Spec.Suspend = copyPointer(spec.Suspend)
		}
		object.Spec.JobTemplate.Spec.Parallelism = copyPointer(spec.Parallelism)
		object.Spec.JobTemplate.Spec.Completions = copyPointer(spec.Completions)
		object.Spec.JobTemplate.Spec.BackoffLimit = copyPointer(spec.BackoffLimit)
		object.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = copyPointer(
			spec.TTLSecondsAfterFinished,
		)
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	default:
		return nil, ErrInvalidInput
	}
}

func applyWorkloadMetadata(metadata *metav1.ObjectMeta, spec WorkloadSpecInput) {
	metadata.Labels = maps.Clone(spec.Labels)
	metadata.Annotations = workloadAnnotations(spec.Annotations, spec.Description)
}

// The modeled parts of a Pod template, written onto the template as it stands.
//
// The selector is immutable, so whatever it matches on is put back over the
// submitted labels: a template that stopped matching its own controller would
// be rejected by the API Server, and on a DaemonSet or a Deployment created
// outside ZKE the matched labels are not the ones this form knows about.
func applyWorkloadPodTemplate(
	template *corev1.PodTemplateSpec,
	selector *metav1.LabelSelector,
	spec WorkloadSpecInput,
) {
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	if selector != nil {
		maps.Copy(labels, selector.MatchLabels)
	}
	// Written by a typed create and matched on by the selector it generated.
	if value, exists := template.Labels[workloadSelectorLabel]; exists {
		labels[workloadSelectorLabel] = value
	}
	template.Labels = labels

	annotations := workloadAnnotations(spec.Annotations, spec.Description)
	// A rolling restart is recorded as an annotation on the template. Dropping
	// it would change the template and so start another rollout — an edit that
	// asked for nothing of the kind.
	if value, exists := template.Annotations[workloadRestartAnnotation]; exists {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[workloadRestartAnnotation] = value
	}
	template.Annotations = annotations

	template.Spec.Containers = mergeWorkloadContainers(
		template.Spec.Containers,
		spec.Containers,
	)
	template.Spec.InitContainers = mergeWorkloadContainers(
		template.Spec.InitContainers,
		spec.InitContainers,
	)
	template.Spec.Volumes = mergeWorkloadVolumes(template.Spec.Volumes, spec.Volumes)
	template.Spec.ImagePullSecrets = workloadImagePullSecretSpec(spec.ImagePullSecrets)
	template.Spec.NodeSelector = maps.Clone(spec.NodeSelector)
	template.Spec.Tolerations = workloadTolerationSpec(spec.Tolerations)
	// These fields were added after typed updates already existed. Omission must
	// preserve them for older clients; an explicit empty object/array clears them.
	if spec.Affinity != nil {
		template.Spec.Affinity = workloadAffinitySpec(spec.Affinity)
	}
	if spec.TopologySpreadConstraints != nil {
		template.Spec.TopologySpreadConstraints = workloadTopologySpreadSpec(spec.TopologySpreadConstraints)
	}
}

// Submitted containers, each carrying forward what the form cannot express.
//
// Matching is by name, which is what identifies a container in a Pod. A
// container the form did not send is gone — removing one is a thing an edit is
// allowed to do — but one that is still there keeps its ports, its security
// context, its termination settings and everything else outside the template.
func mergeWorkloadContainers(
	existing []corev1.Container,
	input []WorkloadContainerTemplate,
) []corev1.Container {
	byName := make(map[string]corev1.Container, len(existing))
	for _, container := range existing {
		byName[container.Name] = container
	}
	result := make([]corev1.Container, 0, len(input))
	for _, container := range input {
		modeled := workloadContainerSpec(container)
		current, found := byName[container.Name]
		if !found {
			result = append(result, modeled)
			continue
		}
		merged := *current.DeepCopy()
		merged.Image = modeled.Image
		merged.ImagePullPolicy = modeled.ImagePullPolicy
		merged.Command = modeled.Command
		merged.Args = modeled.Args
		merged.WorkingDir = modeled.WorkingDir
		merged.Env = mergeWorkloadEnv(current.Env, modeled.Env)
		merged.Resources = modeled.Resources
		merged.VolumeMounts = mergeWorkloadVolumeMounts(current.VolumeMounts, modeled.VolumeMounts)
		merged.LivenessProbe = mergeWorkloadProbe(current.LivenessProbe, modeled.LivenessProbe)
		merged.ReadinessProbe = mergeWorkloadProbe(current.ReadinessProbe, modeled.ReadinessProbe)
		merged.Lifecycle = modeled.Lifecycle
		if container.Ports != nil {
			merged.Ports = mergeWorkloadContainerPorts(current.Ports, modeled.Ports)
		}
		merged.SecurityContext = mergeWorkloadSecurityContext(
			current.SecurityContext,
			container.Privileged,
		)
		result = append(result, merged)
	}
	return result
}

// Container port rows deliberately do not expose hostPort/hostIP. For a row
// that still identifies the same named (or unnamed number/protocol) port, keep
// those node-network fields rather than clearing a YAML-authored reservation.
func mergeWorkloadContainerPorts(
	existing []corev1.ContainerPort,
	input []corev1.ContainerPort,
) []corev1.ContainerPort {
	type key struct {
		name     string
		port     int32
		protocol corev1.Protocol
	}
	portKey := func(port corev1.ContainerPort) key {
		if port.Name != "" {
			return key{name: port.Name}
		}
		return key{port: port.ContainerPort, protocol: port.Protocol}
	}
	byKey := make(map[key]corev1.ContainerPort, len(existing))
	for _, port := range existing {
		byKey[portKey(port)] = port
	}
	result := make([]corev1.ContainerPort, 0, len(input))
	for _, port := range input {
		current, found := byKey[portKey(port)]
		if !found {
			result = append(result, port)
			continue
		}
		current.Name = port.Name
		current.ContainerPort = port.ContainerPort
		current.Protocol = port.Protocol
		result = append(result, current)
	}
	return result
}

// Environment variables, keeping the sources this form does not model.
//
// `fieldRef`, `resourceFieldRef` and whole-object `envFrom` entries come back
// from a read as a name with no value, because there is nothing in the form to
// show them in. Writing that back would turn `POD_IP` into an empty string —
// so a submitted variable with no value and no reference keeps whatever the
// container already had under that name.
func mergeWorkloadEnv(existing []corev1.EnvVar, input []corev1.EnvVar) []corev1.EnvVar {
	byName := make(map[string]corev1.EnvVar, len(existing))
	for _, variable := range existing {
		byName[variable.Name] = variable
	}
	result := make([]corev1.EnvVar, 0, len(input))
	for _, variable := range input {
		if variable.Value == "" && variable.ValueFrom == nil {
			if current, found := byName[variable.Name]; found && current.ValueFrom != nil {
				result = append(result, *current.DeepCopy())
				continue
			}
		}
		result = append(result, variable)
	}
	return result
}

// Mounts, keeping the fields outside the form.
//
// `mountPropagation`, `subPathExpr` and recursive read-only settings are not
// modeled; a mount that is still at the same path in the same volume keeps
// them.
func mergeWorkloadVolumeMounts(
	existing []corev1.VolumeMount,
	input []corev1.VolumeMount,
) []corev1.VolumeMount {
	type key struct{ name, path string }
	byKey := make(map[key]corev1.VolumeMount, len(existing))
	for _, mount := range existing {
		byKey[key{mount.Name, mount.MountPath}] = mount
	}
	result := make([]corev1.VolumeMount, 0, len(input))
	for _, mount := range input {
		current, found := byKey[key{mount.Name, mount.MountPath}]
		if !found {
			result = append(result, mount)
			continue
		}
		merged := *current.DeepCopy()
		merged.SubPath = mount.SubPath
		merged.ReadOnly = mount.ReadOnly
		result = append(result, merged)
	}
	return result
}

// A probe the form could not express is left alone.
//
// A gRPC probe reads back with no handler at all, and a probe without a handler
// is rejected by Kubernetes. Submitting one on a container that already has a
// probe means the form had nothing to show and nothing to change.
func mergeWorkloadProbe(existing *corev1.Probe, input *corev1.Probe) *corev1.Probe {
	if input == nil {
		return nil
	}
	if input.Exec == nil && input.HTTPGet == nil && input.TCPSocket == nil && existing != nil {
		return existing.DeepCopy()
	}
	return input
}

// The security context, of which only `privileged` is modeled.
//
// Everything else an operator set — `runAsUser`, capabilities, seccomp — stays,
// and a context that ends up empty is removed rather than written as an object
// saying nothing.
func mergeWorkloadSecurityContext(
	existing *corev1.SecurityContext,
	privileged *bool,
) *corev1.SecurityContext {
	wanted := privileged != nil && *privileged
	if existing == nil {
		if !wanted {
			return nil
		}
		return &corev1.SecurityContext{Privileged: &wanted}
	}
	result := existing.DeepCopy()
	if wanted {
		result.Privileged = &wanted
	} else {
		result.Privileged = nil
	}
	if reflect.DeepEqual(result, &corev1.SecurityContext{}) {
		return nil
	}
	return result
}

// Volumes, keeping sources the form does not model.
//
// A projected, CSI or downward-API volume reads back as a name with no source,
// because there is no field in the form to hold it. Written back as submitted
// it would become a volume of no kind, and every mount pointing at it would
// fail — so a submitted volume with no modeled source keeps the source the Pod
// already had under that name.
func mergeWorkloadVolumes(existing []corev1.Volume, input []WorkloadVolume) []corev1.Volume {
	byName := make(map[string]corev1.Volume, len(existing))
	for _, volume := range existing {
		byName[volume.Name] = volume
	}
	modeled := workloadVolumeSpec(input)
	result := make([]corev1.Volume, 0, len(modeled))
	for index, volume := range modeled {
		if !workloadVolumeHasSource(input[index]) {
			if current, found := byName[volume.Name]; found {
				result = append(result, *current.DeepCopy())
				continue
			}
		}
		result = append(result, volume)
	}
	return result
}

func workloadVolumeHasSource(volume WorkloadVolume) bool {
	return volume.EmptyDir != nil ||
		volume.HostPath != nil ||
		volume.ConfigMap != nil ||
		volume.Secret != nil ||
		volume.PersistentVolumeClaim != nil ||
		volume.NFS != nil
}
