package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	DefaultResourceListLimit int64 = 100
	MaxResourceListLimit     int64 = 500
)

type ResourceIdentity struct {
	Group    string
	Version  string
	Resource string
}

type ListResourcesInput struct {
	ClusterID       string
	Resource        ResourceIdentity
	Namespace       string
	Limit           int64
	ContinueToken   string
	LabelSelector   string
	FieldSelector   string
	ResourceVersion string
}

type GetResourceInput struct {
	ClusterID string
	Resource  ResourceIdentity
	Namespace string
	Name      string
}

type ResourcePage struct {
	APIVersion         string
	Kind               string
	Items              []map[string]any
	ContinueToken      string
	ResourceVersion    string
	RemainingItemCount *int64
}

func (service *Service) DiscoverResources(
	ctx context.Context,
	clusterID string,
) (kubernetescatalog.Catalog, error) {
	if !validation.IsUUID(clusterID) {
		return kubernetescatalog.Catalog{}, ErrInvalidInput
	}
	var body bytes.Buffer
	response, err := service.requester.RequestResource(
		ctx,
		clusterID,
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
		},
		nil,
		&body,
	)
	if err != nil {
		return kubernetescatalog.Catalog{}, requestError(err)
	}
	if err := responseError(response); err != nil {
		return kubernetescatalog.Catalog{}, err
	}
	var catalog kubernetescatalog.Catalog
	if err := json.Unmarshal(body.Bytes(), &catalog); err != nil {
		return kubernetescatalog.Catalog{}, fmt.Errorf(
			"%w: decode Kubernetes discovery catalog",
			ErrInvalidResponse,
		)
	}
	filtered := make([]kubernetescatalog.Resource, 0, len(catalog.Resources))
	for _, resource := range catalog.Resources {
		if !validResourceIdentity(ResourceIdentity{
			Group:    resource.Group,
			Version:  resource.Version,
			Resource: resource.Resource,
		}) ||
			resource.Kind == "" ||
			len(resource.Kind) > 253 ||
			sensitiveResource(resource.Group, resource.Resource) {
			continue
		}
		resource.Verbs = readableVerbs(resource.Verbs)
		if len(resource.Verbs) == 0 {
			continue
		}
		filtered = append(filtered, resource)
	}
	catalog.Resources = filtered
	return catalog, nil
}

func (service *Service) ListResources(
	ctx context.Context,
	input ListResourcesInput,
) (ResourcePage, error) {
	if err := validateListResourcesInput(input); err != nil {
		return ResourcePage{}, err
	}
	var body bytes.Buffer
	response, err := service.requester.RequestResource(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource:       protocolResource(input.Resource),
			Namespace:      input.Namespace,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			ListOptions: &agentv1.ListOptions{
				LabelSelector:   input.LabelSelector,
				FieldSelector:   input.FieldSelector,
				Limit:           uint64(input.Limit),
				ContinueToken:   input.ContinueToken,
				ResourceVersion: input.ResourceVersion,
			},
		},
		nil,
		&body,
	)
	if err != nil {
		return ResourcePage{}, requestError(err)
	}
	if err := responseErrorWithNotFound(response, ErrResourceNotFound); err != nil {
		return ResourcePage{}, err
	}

	var list unstructured.UnstructuredList
	if err := list.UnmarshalJSON(body.Bytes()); err != nil {
		return ResourcePage{}, fmt.Errorf(
			"%w: decode Kubernetes resource list",
			ErrInvalidResponse,
		)
	}
	groupVersion, err := schema.ParseGroupVersion(list.GetAPIVersion())
	if err != nil ||
		groupVersion.Group != input.Resource.Group ||
		groupVersion.Version != input.Resource.Version ||
		list.GetKind() == "" {
		return ResourcePage{}, fmt.Errorf(
			"%w: Kubernetes resource list identity changed",
			ErrInvalidResponse,
		)
	}
	items := make([]map[string]any, 0, len(list.Items))
	for index := range list.Items {
		if list.Items[index].GetName() == "" {
			return ResourcePage{}, fmt.Errorf(
				"%w: Kubernetes resource list item has no name",
				ErrInvalidResponse,
			)
		}
		stripManagedFields(&list.Items[index])
		items = append(items, list.Items[index].Object)
	}
	return ResourcePage{
		APIVersion:         list.GetAPIVersion(),
		Kind:               list.GetKind(),
		Items:              items,
		ContinueToken:      list.GetContinue(),
		ResourceVersion:    list.GetResourceVersion(),
		RemainingItemCount: list.GetRemainingItemCount(),
	}, nil
}

func (service *Service) GetResource(
	ctx context.Context,
	input GetResourceInput,
) (map[string]any, error) {
	if err := validateGetResourceInput(input); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	response, err := service.requester.RequestResource(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource:       protocolResource(input.Resource),
			Namespace:      input.Namespace,
			Name:           input.Name,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
		&body,
	)
	if err != nil {
		return nil, requestError(err)
	}
	if err := responseErrorWithNotFound(response, ErrResourceNotFound); err != nil {
		return nil, err
	}
	var object unstructured.Unstructured
	if err := object.UnmarshalJSON(body.Bytes()); err != nil ||
		object.GetName() == "" ||
		object.GetName() != input.Name ||
		object.GetKind() == "" ||
		object.GroupVersionKind().Group != input.Resource.Group ||
		object.GroupVersionKind().Version != input.Resource.Version {
		return nil, fmt.Errorf(
			"%w: decode Kubernetes resource detail",
			ErrInvalidResponse,
		)
	}
	stripManagedFields(&object)
	return object.Object, nil
}

func protocolResource(resource ResourceIdentity) *agentv1.GroupVersionResource {
	return &agentv1.GroupVersionResource{
		Group:    resource.Group,
		Version:  resource.Version,
		Resource: resource.Resource,
	}
}

func validateListResourcesInput(input ListResourcesInput) error {
	if !validation.IsUUID(input.ClusterID) ||
		!validResourceIdentity(input.Resource) ||
		sensitiveResource(input.Resource.Group, input.Resource.Resource) ||
		!validNamespace(input.Namespace) ||
		input.Limit < 1 ||
		input.Limit > MaxResourceListLimit ||
		len(input.ContinueToken) > maxContinueTokenBytes ||
		strings.TrimSpace(input.ContinueToken) != input.ContinueToken ||
		len(input.ResourceVersion) > maxContinueTokenBytes ||
		strings.TrimSpace(input.ResourceVersion) != input.ResourceVersion ||
		len(input.LabelSelector) > maxSelectorBytes ||
		len(input.FieldSelector) > maxSelectorBytes {
		return ErrInvalidInput
	}
	if _, err := labels.Parse(input.LabelSelector); err != nil {
		return ErrInvalidInput
	}
	if _, err := fields.ParseSelector(input.FieldSelector); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func validateGetResourceInput(input GetResourceInput) error {
	if !validation.IsUUID(input.ClusterID) ||
		!validResourceIdentity(input.Resource) ||
		sensitiveResource(input.Resource.Group, input.Resource.Resource) ||
		!validNamespace(input.Namespace) ||
		!validPathSegment(input.Name) {
		return ErrInvalidInput
	}
	return nil
}

func validResourceIdentity(resource ResourceIdentity) bool {
	return (resource.Group == "" ||
		len(k8svalidation.IsDNS1123Subdomain(resource.Group)) == 0) &&
		len(k8svalidation.IsDNS1123Subdomain(resource.Version)) == 0 &&
		len(k8svalidation.IsDNS1123Subdomain(resource.Resource)) == 0
}

func validNamespace(namespace string) bool {
	return namespace == "" ||
		len(k8svalidation.IsDNS1123Label(namespace)) == 0
}

func validPathSegment(value string) bool {
	return value != "" &&
		len(value) <= 253 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "/?#")
}

func sensitiveResource(group string, resource string) bool {
	return group == "" && resource == "secrets"
}

func readableVerbs(verbs []string) []string {
	result := make([]string, 0, 2)
	for _, expected := range []string{"get", "list"} {
		for _, verb := range verbs {
			if verb == expected {
				result = append(result, expected)
				break
			}
		}
	}
	return result
}

func stripManagedFields(object *unstructured.Unstructured) {
	object.SetManagedFields(nil)
}
