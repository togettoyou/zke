package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	DefaultResourceListLimit int64 = 100
	MaxResourceListLimit     int64 = 500
	maxFieldManagerBytes           = 128
	minIdempotencyKeyBytes         = 16
	maxIdempotencyKeyBytes         = 128
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

type MutationOptions struct {
	DryRun       bool
	FieldManager string
	Force        bool
}

type CreateResourceInput struct {
	ClusterID      string
	Resource       ResourceIdentity
	Namespace      string
	Object         map[string]any
	Options        MutationOptions
	Confirm        bool
	IdempotencyKey string
}

type UpdateResourceInput struct {
	ClusterID      string
	Resource       ResourceIdentity
	Namespace      string
	Name           string
	Object         map[string]any
	Options        MutationOptions
	Confirm        bool
	IdempotencyKey string
}

type PatchResourceInput struct {
	ClusterID      string
	Resource       ResourceIdentity
	Namespace      string
	Name           string
	PatchType      agentv1.PatchType
	Patch          json.RawMessage
	Options        MutationOptions
	Confirm        bool
	IdempotencyKey string
}

type DeletePreconditions struct {
	UID             string
	ResourceVersion string
}

type DeleteResourceInput struct {
	ClusterID          string
	Resource           ResourceIdentity
	Namespace          string
	Name               string
	DryRun             bool
	Confirm            bool
	GracePeriodSeconds *int64
	Propagation        agentv1.DeletePropagation
	Preconditions      DeletePreconditions
	IdempotencyKey     string
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
	body := service.newResponseBuffer()
	defer body.Release()
	response, err := service.requester.RequestResource(
		ctx,
		clusterID,
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
		},
		nil,
		body,
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
		resource.Verbs = supportedVerbs(resource.Verbs)
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
	body := service.newResponseBuffer()
	defer body.Release()
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
		body,
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
	body := service.newResponseBuffer()
	defer body.Release()
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
		body,
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

func (service *Service) CreateResource(
	ctx context.Context,
	input CreateResourceInput,
) (map[string]any, error) {
	object, err := validatedMutationObject(
		input.ClusterID,
		input.Resource,
		input.Namespace,
		"",
		input.Object,
		false,
		input.Options,
		input.Confirm,
		input.IdempotencyKey,
	)
	if err != nil {
		return nil, err
	}
	body, err := object.MarshalJSON()
	if err != nil {
		return nil, ErrInvalidInput
	}
	return service.sendObjectMutation(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			Resource:        protocolResource(input.Resource),
			Namespace:       input.Namespace,
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        uint64(len(body)),
			MutationOptions: protocolMutationOptions(input.Options),
		},
		body,
		input.IdempotencyKey,
		input.Resource,
		input.Namespace,
		object.GetName(),
	)
}

func (service *Service) UpdateResource(
	ctx context.Context,
	input UpdateResourceInput,
) (map[string]any, error) {
	object, err := validatedMutationObject(
		input.ClusterID,
		input.Resource,
		input.Namespace,
		input.Name,
		input.Object,
		true,
		input.Options,
		input.Confirm,
		input.IdempotencyKey,
	)
	if err != nil || object.GetResourceVersion() == "" {
		return nil, ErrInvalidInput
	}
	body, err := object.MarshalJSON()
	if err != nil {
		return nil, ErrInvalidInput
	}
	return service.sendObjectMutation(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			Resource:        protocolResource(input.Resource),
			Namespace:       input.Namespace,
			Name:            input.Name,
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        uint64(len(body)),
			MutationOptions: protocolMutationOptions(input.Options),
		},
		body,
		input.IdempotencyKey,
		input.Resource,
		input.Namespace,
		input.Name,
	)
}

func (service *Service) PatchResource(
	ctx context.Context,
	input PatchResourceInput,
) (map[string]any, error) {
	if err := validateMutationBase(
		input.ClusterID,
		input.Resource,
		input.Namespace,
		input.Name,
		input.Options,
		input.Confirm,
		input.IdempotencyKey,
	); err != nil ||
		!validPatchType(input.PatchType) ||
		len(input.Patch) == 0 ||
		(input.PatchType == agentv1.PatchType_PATCH_TYPE_APPLY &&
			input.Options.FieldManager == "") ||
		(input.PatchType != agentv1.PatchType_PATCH_TYPE_APPLY &&
			input.Options.Force) ||
		validatePatchIdentity(input) != nil {
		return nil, ErrInvalidInput
	}
	body := bytes.Clone(input.Patch)
	if input.PatchType == agentv1.PatchType_PATCH_TYPE_APPLY {
		var patchObject map[string]any
		if json.Unmarshal(input.Patch, &patchObject) != nil {
			return nil, ErrInvalidInput
		}
		object, err := validatedMutationObject(
			input.ClusterID,
			input.Resource,
			input.Namespace,
			input.Name,
			patchObject,
			true,
			input.Options,
			input.Confirm,
			input.IdempotencyKey,
		)
		if err != nil {
			return nil, err
		}
		body, err = object.MarshalJSON()
		if err != nil {
			return nil, ErrInvalidInput
		}
	}
	return service.sendObjectMutation(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_PATCH,
			Resource:        protocolResource(input.Resource),
			Namespace:       input.Namespace,
			Name:            input.Name,
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			PatchType:       input.PatchType,
			BodySize:        uint64(len(body)),
			MutationOptions: protocolMutationOptions(input.Options),
		},
		body,
		input.IdempotencyKey,
		input.Resource,
		input.Namespace,
		input.Name,
	)
}

func (service *Service) DeleteResource(
	ctx context.Context,
	input DeleteResourceInput,
) error {
	if !validation.IsUUID(input.ClusterID) ||
		!validResourceIdentity(input.Resource) ||
		sensitiveResource(input.Resource.Group, input.Resource.Resource) ||
		!validNamespace(input.Namespace) ||
		!validPathSegment(input.Name) ||
		(!input.DryRun && !input.Confirm) ||
		!validIdempotencyKey(input.IdempotencyKey) ||
		(input.GracePeriodSeconds != nil && *input.GracePeriodSeconds < 0) ||
		input.Propagation > agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND ||
		!validPrecondition(input.Preconditions.UID, 128) ||
		!validPrecondition(input.Preconditions.ResourceVersion, 256) {
		return ErrInvalidInput
	}
	requester, ok := service.requester.(MutationResourceRequester)
	if !ok {
		return ErrAgentUnsupported
	}
	options := &agentv1.DeleteOptions{
		DryRun:             input.DryRun,
		GracePeriodSeconds: input.GracePeriodSeconds,
		Propagation:        input.Propagation,
	}
	if input.Preconditions.UID != "" ||
		input.Preconditions.ResourceVersion != "" {
		options.Preconditions = &agentv1.ResourcePreconditions{
			Uid:             input.Preconditions.UID,
			ResourceVersion: input.Preconditions.ResourceVersion,
		}
	}
	response, err := requester.RequestResourceMutation(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:          agentv1.ResourceVerb_RESOURCE_VERB_DELETE,
			Resource:      protocolResource(input.Resource),
			Namespace:     input.Namespace,
			Name:          input.Name,
			DeleteOptions: options,
		},
		nil,
		io.Discard,
		input.IdempotencyKey,
	)
	if err != nil {
		return requestError(err)
	}
	return mutationResponseError(response, false)
}

func validatedMutationObject(
	clusterID string,
	resource ResourceIdentity,
	namespace string,
	name string,
	input map[string]any,
	nameRequired bool,
	options MutationOptions,
	confirm bool,
	idempotencyKey string,
) (*unstructured.Unstructured, error) {
	if err := validateMutationBase(
		clusterID,
		resource,
		namespace,
		name,
		options,
		confirm,
		idempotencyKey,
	); err != nil || input == nil {
		return nil, ErrInvalidInput
	}
	copied, ok := runtime.DeepCopyJSONValue(input).(map[string]any)
	if !ok {
		return nil, ErrInvalidInput
	}
	object := &unstructured.Unstructured{Object: copied}
	gvk := object.GroupVersionKind()
	if object.GetKind() == "" ||
		gvk.Group != resource.Group ||
		gvk.Version != resource.Version ||
		object.GetNamespace() != namespace ||
		object.GetGenerateName() != "" ||
		!validPathSegment(object.GetName()) {
		return nil, ErrInvalidInput
	}
	if nameRequired && object.GetName() != name {
		return nil, ErrInvalidInput
	}
	object.SetManagedFields(nil)
	return object, nil
}

func validateMutationBase(
	clusterID string,
	resource ResourceIdentity,
	namespace string,
	name string,
	options MutationOptions,
	confirm bool,
	idempotencyKey string,
) error {
	if !validation.IsUUID(clusterID) ||
		!validResourceIdentity(resource) ||
		sensitiveResource(resource.Group, resource.Resource) ||
		!validNamespace(namespace) ||
		(name != "" && !validPathSegment(name)) ||
		(!options.DryRun && !confirm) ||
		!validIdempotencyKey(idempotencyKey) ||
		len(options.FieldManager) > maxFieldManagerBytes ||
		strings.TrimSpace(options.FieldManager) != options.FieldManager {
		return ErrInvalidInput
	}
	return nil
}

func validIdempotencyKey(value string) bool {
	return len(value) >= minIdempotencyKeyBytes &&
		len(value) <= maxIdempotencyKeyBytes &&
		strings.TrimSpace(value) == value
}

func validPrecondition(value string, maximum int) bool {
	return len(value) <= maximum && strings.TrimSpace(value) == value
}

func validPatchType(value agentv1.PatchType) bool {
	return value >= agentv1.PatchType_PATCH_TYPE_JSON &&
		value <= agentv1.PatchType_PATCH_TYPE_APPLY
}

func validatePatchIdentity(input PatchResourceInput) error {
	if input.PatchType == agentv1.PatchType_PATCH_TYPE_JSON {
		var operations []struct {
			Operation string `json:"op"`
			Path      string `json:"path"`
			From      string `json:"from"`
		}
		if json.Unmarshal(input.Patch, &operations) != nil ||
			len(operations) == 0 {
			return ErrInvalidInput
		}
		for _, operation := range operations {
			if operation.Operation == "" ||
				unsafeIdentityPatchPath(operation.Path) ||
				(operation.From != "" &&
					unsafeIdentityPatchPath(operation.From)) {
				return ErrInvalidInput
			}
		}
		return nil
	}
	var patch map[string]any
	if json.Unmarshal(input.Patch, &patch) != nil {
		return ErrInvalidInput
	}
	if input.PatchType == agentv1.PatchType_PATCH_TYPE_APPLY {
		_, err := validatedMutationObject(
			input.ClusterID,
			input.Resource,
			input.Namespace,
			input.Name,
			patch,
			true,
			input.Options,
			input.Confirm,
			input.IdempotencyKey,
		)
		return err
	}
	if value, exists := patch["apiVersion"]; exists {
		apiVersion, ok := value.(string)
		if !ok || apiVersion != expectedAPIVersion(input.Resource) {
			return ErrInvalidInput
		}
	}
	if value, exists := patch["kind"]; exists {
		kind, ok := value.(string)
		if !ok || strings.TrimSpace(kind) == "" {
			return ErrInvalidInput
		}
	}
	if rawMetadata, exists := patch["metadata"]; exists {
		metadata, ok := rawMetadata.(map[string]any)
		if !ok {
			return ErrInvalidInput
		}
		if value, exists := metadata["name"]; exists && value != input.Name {
			return ErrInvalidInput
		}
		if value, exists := metadata["namespace"]; exists &&
			value != input.Namespace {
			return ErrInvalidInput
		}
		if _, exists := metadata["generateName"]; exists {
			return ErrInvalidInput
		}
	}
	return nil
}

func unsafeIdentityPatchPath(path string) bool {
	for _, protected := range []string{
		"/apiVersion",
		"/kind",
		"/metadata/name",
		"/metadata/namespace",
		"/metadata/generateName",
	} {
		if path == protected || strings.HasPrefix(path, protected+"/") {
			return true
		}
	}
	return false
}

func protocolMutationOptions(options MutationOptions) *agentv1.MutationOptions {
	return &agentv1.MutationOptions{
		DryRun:       options.DryRun,
		FieldManager: options.FieldManager,
		Force:        options.Force,
	}
}

func (service *Service) sendObjectMutation(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	body []byte,
	idempotencyKey string,
	resource ResourceIdentity,
	namespace string,
	name string,
) (map[string]any, error) {
	requester, ok := service.requester.(MutationResourceRequester)
	if !ok {
		return nil, ErrAgentUnsupported
	}
	responseBody := service.newResponseBuffer()
	defer responseBody.Release()
	response, err := requester.RequestResourceMutation(
		ctx,
		clusterID,
		request,
		bytes.NewReader(body),
		responseBody,
		idempotencyKey,
	)
	if err != nil {
		return nil, requestError(err)
	}
	if err := mutationResponseError(response, true); err != nil {
		return nil, err
	}
	var object unstructured.Unstructured
	if object.UnmarshalJSON(responseBody.Bytes()) != nil ||
		object.GetName() != name ||
		object.GetKind() == "" ||
		object.GetNamespace() != namespace ||
		object.GroupVersionKind().Group != resource.Group ||
		object.GroupVersionKind().Version != resource.Version {
		return nil, fmt.Errorf(
			"%w: Kubernetes mutation response identity changed",
			ErrInvalidResponse,
		)
	}
	stripManagedFields(&object)
	return object.Object, nil
}

func mutationResponseError(
	response *agentv1.ResourceResponse,
	expectBody bool,
) error {
	if response == nil {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if response.GetKubernetesStatusCode() < 200 ||
			response.GetKubernetesStatusCode() >= 300 ||
			(expectBody &&
				(response.GetContentType() != kubernetesJSONContentType ||
					response.GetBodySize() == 0)) ||
			(!expectBody && response.GetBodySize() != 0) {
			return ErrInvalidResponse
		}
		return nil
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_CONFLICT &&
		response.GetReason() == "IdempotencyConflict" {
		return ErrIdempotencyConflict
	}
	return responseErrorWithNotFound(response, ErrResourceNotFound)
}

func expectedAPIVersion(resource ResourceIdentity) string {
	if resource.Group == "" {
		return resource.Version
	}
	return resource.Group + "/" + resource.Version
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

func supportedVerbs(verbs []string) []string {
	result := make([]string, 0, 6)
	for _, expected := range []string{
		"get",
		"list",
		"create",
		"update",
		"patch",
		"delete",
	} {
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
