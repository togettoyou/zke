package kubernetesresource

import (
	"context"
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var namespaceIdentity = ResourceIdentity{
	Version:  "v1",
	Resource: "namespaces",
}

type ListNamespacesInput struct {
	ClusterID     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type NamespacePage struct {
	Namespaces         []NamespaceSummary `json:"namespaces"`
	ContinueToken      string             `json:"continue_token"`
	ResourceVersion    string             `json:"resource_version"`
	RemainingItemCount *int64             `json:"remaining_item_count"`
}

type NamespaceSummary struct {
	Name              string            `json:"name"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resource_version"`
	CreationTimestamp time.Time         `json:"creation_timestamp"`
	Phase             string            `json:"phase"`
	Labels            map[string]string `json:"labels"`
}

type NamespaceDetail struct {
	NamespaceSummary
	Annotations map[string]string `json:"annotations"`
	Finalizers  []string          `json:"finalizers"`
}

type CreateNamespaceInput struct {
	ClusterID      string
	Name           string
	Labels         map[string]string
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type DeleteNamespaceInput struct {
	ClusterID       string
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func (service *Service) ListNamespaces(
	ctx context.Context,
	input ListNamespacesInput,
) (NamespacePage, error) {
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID:       input.ClusterID,
		Resource:        namespaceIdentity,
		Limit:           input.Limit,
		ContinueToken:   input.ContinueToken,
		LabelSelector:   input.LabelSelector,
		FieldSelector:   input.FieldSelector,
		ResourceVersion: "",
	})
	if err != nil {
		return NamespacePage{}, err
	}
	result := NamespacePage{
		Namespaces:         make([]NamespaceSummary, 0, len(page.Items)),
		ContinueToken:      page.ContinueToken,
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := namespaceDetail(item)
		if err != nil {
			return NamespacePage{}, err
		}
		result.Namespaces = append(result.Namespaces, detail.NamespaceSummary)
	}
	return result, nil
}

func (service *Service) GetNamespace(
	ctx context.Context,
	clusterID string,
	name string,
) (NamespaceDetail, error) {
	if len(k8svalidation.IsDNS1123Label(name)) != 0 {
		return NamespaceDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID,
		Resource:  namespaceIdentity,
		Name:      name,
	})
	if err != nil {
		return NamespaceDetail{}, err
	}
	return namespaceDetail(object)
}

func (service *Service) CreateNamespace(
	ctx context.Context,
	input CreateNamespaceInput,
) (NamespaceDetail, error) {
	if len(k8svalidation.IsDNS1123Label(input.Name)) != 0 ||
		!validNamespaceLabels(input.Labels) {
		return NamespaceDetail{}, ErrInvalidInput
	}
	labels := make(map[string]any, len(input.Labels))
	for key, value := range input.Labels {
		labels[key] = value
	}
	object, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID,
		Resource:  namespaceIdentity,
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]any{
				"name":   input.Name,
				"labels": labels,
			},
		},
		Options:        MutationOptions{DryRun: input.DryRun},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return NamespaceDetail{}, err
	}
	return namespaceDetail(object)
}

func validNamespaceLabels(labels map[string]string) bool {
	for key, value := range labels {
		if len(k8svalidation.IsQualifiedName(key)) != 0 ||
			len(k8svalidation.IsValidLabelValue(value)) != 0 {
			return false
		}
	}
	return true
}

func (service *Service) DeleteNamespace(
	ctx context.Context,
	input DeleteNamespaceInput,
) error {
	if len(k8svalidation.IsDNS1123Label(input.Name)) != 0 {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID,
		Resource:  namespaceIdentity,
		Name:      input.Name,
		DryRun:    input.DryRun,
		Confirm:   input.Confirm,
		Preconditions: DeletePreconditions{
			UID:             input.UID,
			ResourceVersion: input.ResourceVersion,
		},
		IdempotencyKey: input.IdempotencyKey,
	})
}

func namespaceDetail(object map[string]any) (NamespaceDetail, error) {
	resource := &unstructured.Unstructured{Object: object}
	if resource.GetName() == "" ||
		resource.GetKind() != "Namespace" ||
		resource.GetAPIVersion() != "v1" ||
		resource.GetNamespace() != "" {
		return NamespaceDetail{}, ErrInvalidResponse
	}
	phase, _, err := unstructured.NestedString(
		resource.Object,
		"status",
		"phase",
	)
	if err != nil {
		return NamespaceDetail{}, ErrInvalidResponse
	}
	labels := maps.Clone(resource.GetLabels())
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := maps.Clone(resource.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	return NamespaceDetail{
		NamespaceSummary: NamespaceSummary{
			Name:              resource.GetName(),
			UID:               string(resource.GetUID()),
			ResourceVersion:   resource.GetResourceVersion(),
			CreationTimestamp: resource.GetCreationTimestamp().Time,
			Phase:             phase,
			Labels:            labels,
		},
		Annotations: annotations,
		Finalizers: append(
			make([]string, 0, len(resource.GetFinalizers())),
			resource.GetFinalizers()...,
		),
	}, nil
}
