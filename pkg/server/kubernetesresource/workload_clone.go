package kubernetesresource

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CloneWorkloadInput identifies the source snapshot and the new object.
//
// UID and resourceVersion are fixed when the operator opens the clone action.
// A source that changes before either the DryRun or confirmed create is a
// conflict: cloning a newer raw object underneath an older preview would mix
// two different configurations.
type CloneWorkloadInput struct {
	ClusterID             string
	Namespace             string
	Resource              WorkloadResource
	SourceName            string
	SourceUID             string
	SourceResourceVersion string
	Name                  string

	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

func (service *Service) CloneWorkload(
	ctx context.Context,
	input CloneWorkloadInput,
) (WorkloadDetail, error) {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists || validateCloneWorkloadInput(input) != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Name:      input.SourceName,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.SourceUID ||
		current.GetResourceVersion() != input.SourceResourceVersion {
		return WorkloadDetail{}, ErrUpstreamConflict
	}

	object, err := cloneWorkloadObject(existing, input)
	if err != nil {
		return WorkloadDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Object:    object,
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

func validateCloneWorkloadInput(input CloneWorkloadInput) error {
	if !validWorkloadTarget(input.Namespace, input.SourceName, input.Resource) ||
		!validWorkloadTarget(input.Namespace, input.Name, input.Resource) ||
		input.SourceName == input.Name ||
		!validWorkloadMutationIdentity(input.SourceUID, input.SourceResourceVersion) {
		return ErrInvalidInput
	}
	return nil
}

// cloneWorkloadObject keeps the source's complete raw spec, including fields
// the typed form does not model, while removing all server-assigned identity.
// Selectors are rebuilt so the clone cannot adopt the source workload's Pods.
func cloneWorkloadObject(
	existing map[string]any,
	input CloneWorkloadInput,
) (map[string]any, error) {
	object := (&unstructured.Unstructured{Object: existing}).DeepCopy().Object
	if !sanitizeClonedObjectMetadata(object, input.Name, input.Namespace, input.Resource) {
		return nil, ErrInvalidResponse
	}
	unstructured.RemoveNestedField(object, "status")

	selectorLabels := map[string]any{
		workloadSelectorLabel: workloadSelectorValue(input.Resource, input.Name),
	}
	switch input.Resource {
	case WorkloadDeployments, WorkloadStatefulSets, WorkloadDaemonSets:
		if err := unstructured.SetNestedMap(object, map[string]any{
			"matchLabels": selectorLabels,
		}, "spec", "selector"); err != nil {
			return nil, ErrInvalidResponse
		}
		if !prepareClonedPodTemplate(object, selectorLabels, "spec", "template") {
			return nil, ErrInvalidResponse
		}
	case WorkloadJobs:
		unstructured.RemoveNestedField(object, "spec", "selector")
		unstructured.RemoveNestedField(object, "spec", "manualSelector")
		if !prepareClonedPodTemplate(object, selectorLabels, "spec", "template") {
			return nil, ErrInvalidResponse
		}
	case WorkloadCronJobs:
		unstructured.RemoveNestedField(object, "spec", "jobTemplate", "spec", "selector")
		unstructured.RemoveNestedField(object, "spec", "jobTemplate", "spec", "manualSelector")
		if !sanitizeEmbeddedMetadata(object, "spec", "jobTemplate", "metadata") ||
			!prepareClonedPodTemplate(
				object,
				selectorLabels,
				"spec", "jobTemplate", "spec", "template",
			) {
			return nil, ErrInvalidResponse
		}
	default:
		return nil, ErrInvalidInput
	}
	return object, nil
}

func sanitizeClonedObjectMetadata(
	object map[string]any,
	name string,
	namespace string,
	resource WorkloadResource,
) bool {
	metadata, found, err := unstructured.NestedMap(object, "metadata")
	if err != nil || !found {
		return false
	}
	sanitizeMetadata(metadata)
	metadata["name"] = name
	metadata["namespace"] = namespace
	delete(metadata, "generateName")
	removeCloneAnnotations(metadata)
	removeCloneLabels(metadata, resource == WorkloadJobs)
	return unstructured.SetNestedMap(object, metadata, "metadata") == nil
}

func sanitizeEmbeddedMetadata(object map[string]any, fields ...string) bool {
	metadata, found, err := unstructured.NestedMap(object, fields...)
	if err != nil {
		return false
	}
	if !found {
		return true
	}
	removeCloneAnnotations(metadata)
	removeCloneLabels(metadata, true)
	return unstructured.SetNestedMap(object, metadata, fields...) == nil
}

func sanitizeMetadata(metadata map[string]any) {
	for _, field := range []string{
		"uid",
		"resourceVersion",
		"generation",
		"creationTimestamp",
		"deletionTimestamp",
		"deletionGracePeriodSeconds",
		"managedFields",
		"selfLink",
		"ownerReferences",
		"finalizers",
	} {
		delete(metadata, field)
	}
}

func removeCloneAnnotations(metadata map[string]any) {
	annotations, ok := metadata["annotations"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range []string{
		workloadRestartAnnotation,
		"deployment.kubernetes.io/revision",
		"deprecated.daemonset.template.generation",
		"kubectl.kubernetes.io/last-applied-configuration",
		batchv1.JobTrackingFinalizer,
	} {
		delete(annotations, name)
	}
	if len(annotations) == 0 {
		delete(metadata, "annotations")
	}
}

func removeCloneLabels(metadata map[string]any, jobController bool) {
	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		return
	}
	delete(labels, workloadSelectorLabel)
	if jobController {
		for _, name := range []string{
			batchv1.JobNameLabel,
			batchv1.ControllerUidLabel,
			"job-name",
			"controller-uid",
		} {
			delete(labels, name)
		}
	}
	if len(labels) == 0 {
		delete(metadata, "labels")
	}
}

func prepareClonedPodTemplate(
	object map[string]any,
	selectorLabels map[string]any,
	fields ...string,
) bool {
	metadataFields := append(append([]string{}, fields...), "metadata")
	metadata, found, err := unstructured.NestedMap(object, metadataFields...)
	if err != nil || !found {
		return false
	}
	removeCloneAnnotations(metadata)
	removeCloneLabels(metadata, true)

	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
	}
	for name, value := range selectorLabels {
		labels[name] = value
	}
	metadata["labels"] = labels
	return unstructured.SetNestedMap(object, metadata, metadataFields...) == nil
}
