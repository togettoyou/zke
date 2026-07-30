package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

const kubernetesJSONContentType = "application/json"

// newKubernetesResourceHandler adapts the generic Resource Stream contract to
// client-go's dynamic client. The handler intentionally returns Kubernetes
// failures as ResourceResponse values: only malformed Stream traffic resets a
// Stream, while an API authorization or lookup failure is a normal business
// response.
func newKubernetesResourceHandler(
	client dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	maxBodyBytes uint64,
) func(
	context.Context,
	*agentv1.ResourceRequest,
	io.Reader,
) (*agentv1.ResourceResponse, io.Reader, error) {
	if maxBodyBytes == 0 {
		maxBodyBytes = agentprotocol.DefaultMaxResourceBodySize
	}
	mutationCache := newResourceMutationCache()
	return func(
		ctx context.Context,
		request *agentv1.ResourceRequest,
		requestBody io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
			return discoverKubernetesResources(
				ctx,
				discoveryClient,
				maxBodyBytes,
			)
		}
		if client == nil {
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				http.StatusServiceUnavailable,
				"DynamicClientUnavailable",
				"Kubernetes dynamic client is unavailable",
			), nil, nil
		}
		if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DELETE &&
			request.GetRepresentation() !=
				agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT {
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"UnsupportedRepresentation",
				"requested Kubernetes resource representation is unsupported",
			), nil, nil
		}
		if !allowedKubernetesResourceRequest(request) {
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
				http.StatusForbidden,
				"ResourceNotAllowed",
				"requested Kubernetes resource is not enabled for this Agent",
			), nil, nil
		}

		resource := request.GetResource()
		resourceClient := client.Resource(schema.GroupVersionResource{
			Group:    resource.GetGroup(),
			Version:  resource.GetVersion(),
			Resource: resource.GetResource(),
		})
		var target dynamic.ResourceInterface = resourceClient
		if request.GetNamespace() != "" {
			target = resourceClient.Namespace(request.GetNamespace())
		}

		var object any
		var err error
		switch request.GetVerb() {
		case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
			options := metav1.ListOptions{}
			if listOptions := request.GetListOptions(); listOptions != nil {
				options = metav1.ListOptions{
					LabelSelector:   listOptions.GetLabelSelector(),
					FieldSelector:   listOptions.GetFieldSelector(),
					Limit:           int64(listOptions.GetLimit()),
					Continue:        listOptions.GetContinueToken(),
					ResourceVersion: listOptions.GetResourceVersion(),
				}
			}
			object, err = target.List(ctx, options)
		case agentv1.ResourceVerb_RESOURCE_VERB_GET:
			subresources := []string(nil)
			if request.GetSubresource() != "" {
				subresources = append(subresources, request.GetSubresource())
			}
			object, err = target.Get(
				ctx,
				request.GetName(),
				metav1.GetOptions{},
				subresources...,
			)
		case agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			agentv1.ResourceVerb_RESOURCE_VERB_PATCH,
			agentv1.ResourceVerb_RESOURCE_VERB_DELETE:
			body, readErr := readResourceMutationBody(request, requestBody)
			if readErr != nil {
				return nil, nil, readErr
			}
			fingerprint, fingerprintErr := mutationFingerprint(request, body)
			if fingerprintErr != nil {
				return nil, nil, errors.New(
					"fingerprint Kubernetes resource mutation",
				)
			}
			execute := func() (resourceMutationResult, error) {
				return executeKubernetesResourceMutation(
					ctx,
					target,
					request,
					body,
					maxBodyBytes,
				)
			}
			var result resourceMutationResult
			var conflict bool
			var executeErr error
			if key := agentprotocol.ResourceIdempotencyKey(ctx); key != "" {
				result, conflict, executeErr = mutationCache.do(
					ctx,
					key,
					fingerprint,
					execute,
				)
			} else {
				result, executeErr = execute()
			}
			if executeErr != nil {
				return nil, nil, executeErr
			}
			if conflict {
				return resourceErrorResponse(
					agentv1.ResultCode_RESULT_CODE_CONFLICT,
					http.StatusConflict,
					"IdempotencyConflict",
					"idempotency key was already used for another request",
				), nil, nil
			}
			if len(result.body) == 0 {
				return result.response, nil, nil
			}
			return result.response, bytes.NewReader(result.body), nil
		default:
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusMethodNotAllowed,
				"UnsupportedVerb",
				"requested Kubernetes resource verb is unsupported",
			), nil, nil
		}
		if err != nil {
			return kubernetesResourceError(err), nil, nil
		}

		body, err := json.Marshal(object)
		if err != nil {
			return nil, nil, errors.New("marshal Kubernetes resource response")
		}
		if uint64(len(body)) > maxBodyBytes {
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				http.StatusRequestEntityTooLarge,
				"ResponseTooLarge",
				"Kubernetes resource response exceeds the configured limit",
			), nil, nil
		}
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			ContentType:          kubernetesJSONContentType,
			BodySize:             uint64(len(body)),
		}, bytes.NewReader(body), nil
	}
}

// allowedKubernetesResourceRequest is deliberately narrower than the generic
// Resource protocol. Phase 2 only permits List/Get on primary, non-Secret
// resources; the Agent ServiceAccount RBAC remains the final resource-specific
// authorization boundary.
func allowedKubernetesResourceRequest(request *agentv1.ResourceRequest) bool {
	resource := request.GetResource()
	return resource != nil &&
		request.GetSubresource() == "" &&
		!sensitiveKubernetesResource(
			resource.GetGroup(),
			resource.GetResource(),
		) &&
		supportedKubernetesResourceVerb(request.GetVerb())
}

func supportedKubernetesResourceVerb(verb agentv1.ResourceVerb) bool {
	switch verb {
	case agentv1.ResourceVerb_RESOURCE_VERB_LIST,
		agentv1.ResourceVerb_RESOURCE_VERB_GET,
		agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
		agentv1.ResourceVerb_RESOURCE_VERB_PATCH,
		agentv1.ResourceVerb_RESOURCE_VERB_DELETE:
		return true
	default:
		return false
	}
}

func sensitiveKubernetesResource(group string, resource string) bool {
	return group == "" && resource == "secrets"
}

func discoverKubernetesResources(
	ctx context.Context,
	client discovery.DiscoveryInterface,
	maxBodyBytes uint64,
) (*agentv1.ResourceResponse, io.Reader, error) {
	if client == nil {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			http.StatusServiceUnavailable,
			"DiscoveryClientUnavailable",
			"Kubernetes discovery client is unavailable",
		), nil, nil
	}
	if err := ctx.Err(); err != nil {
		return kubernetesResourceError(err), nil, nil
	}
	_, lists, discoveryErr := client.ServerGroupsAndResources()
	if err := ctx.Err(); err != nil {
		return kubernetesResourceError(err), nil, nil
	}
	if len(lists) == 0 && discoveryErr != nil {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			http.StatusServiceUnavailable,
			"DiscoveryFailed",
			"Kubernetes API discovery failed",
		), nil, nil
	}

	catalog := kubernetescatalog.Catalog{
		Resources: make([]kubernetescatalog.Resource, 0),
		Partial:   discoveryErr != nil,
	}
	seen := make(map[string]struct{})
	for _, list := range lists {
		if list == nil {
			continue
		}
		groupVersion, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			catalog.Partial = true
			continue
		}
		for _, apiResource := range list.APIResources {
			if apiResource.Name == "" ||
				apiResource.Kind == "" ||
				strings.Contains(apiResource.Name, "/") {
				continue
			}
			group := groupVersion.Group
			version := groupVersion.Version
			if apiResource.Group != "" {
				group = apiResource.Group
			}
			if apiResource.Version != "" {
				version = apiResource.Version
			}
			if sensitiveKubernetesResource(group, apiResource.Name) {
				continue
			}
			key := group + "\x00" + version + "\x00" + apiResource.Name
			if _, exists := seen[key]; exists {
				continue
			}
			verbs := supportedKubernetesVerbs(apiResource.Verbs)
			if len(verbs) == 0 {
				continue
			}
			seen[key] = struct{}{}
			catalog.Resources = append(
				catalog.Resources,
				kubernetescatalog.Resource{
					Group:      group,
					Version:    version,
					Resource:   apiResource.Name,
					Kind:       apiResource.Kind,
					Namespaced: apiResource.Namespaced,
					Verbs:      verbs,
					ShortNames: append([]string(nil), apiResource.ShortNames...),
					Categories: append([]string(nil), apiResource.Categories...),
				},
			)
		}
	}
	sort.Slice(catalog.Resources, func(left int, right int) bool {
		a := catalog.Resources[left]
		b := catalog.Resources[right]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Resource < b.Resource
	})

	body, err := json.Marshal(catalog)
	if err != nil {
		return nil, nil, errors.New("marshal Kubernetes discovery response")
	}
	if uint64(len(body)) > maxBodyBytes {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
			http.StatusRequestEntityTooLarge,
			"ResponseTooLarge",
			"Kubernetes discovery response exceeds the configured limit",
		), nil, nil
	}
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: http.StatusOK,
		ContentType:          kubernetesJSONContentType,
		BodySize:             uint64(len(body)),
	}, bytes.NewReader(body), nil
}

func supportedKubernetesVerbs(verbs metav1.Verbs) []string {
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

func readResourceMutationBody(
	request *agentv1.ResourceRequest,
	reader io.Reader,
) ([]byte, error) {
	if request.GetBodySize() == 0 {
		return nil, nil
	}
	if reader == nil {
		return nil, agentprotocol.ErrStreamProtocol
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if uint64(len(body)) != request.GetBodySize() {
		return nil, agentprotocol.ErrStreamProtocol
	}
	return body, nil
}

func executeKubernetesResourceMutation(
	ctx context.Context,
	target dynamic.ResourceInterface,
	request *agentv1.ResourceRequest,
	body []byte,
	maxBodyBytes uint64,
) (resourceMutationResult, error) {
	var (
		object     *unstructured.Unstructured
		statusCode = http.StatusOK
		err        error
	)
	switch request.GetVerb() {
	case agentv1.ResourceVerb_RESOURCE_VERB_CREATE:
		object, err = decodeMutationObject(request, body, false)
		if err != nil {
			return invalidMutationResult(
				"InvalidObject",
				"Kubernetes resource identity does not match the request",
			), nil
		}
		object, err = target.Create(
			ctx,
			object,
			createOptions(request.GetMutationOptions()),
		)
		statusCode = http.StatusCreated
	case agentv1.ResourceVerb_RESOURCE_VERB_UPDATE:
		object, err = decodeMutationObject(request, body, true)
		if err != nil {
			return invalidMutationResult(
				"InvalidObject",
				"Kubernetes resource identity does not match the request",
			), nil
		}
		if object.GetResourceVersion() == "" {
			return invalidMutationResult(
				"ResourceVersionRequired",
				"metadata.resourceVersion is required for update",
			), nil
		}
		object, err = target.Update(
			ctx,
			object,
			updateOptions(request.GetMutationOptions()),
		)
	case agentv1.ResourceVerb_RESOURCE_VERB_PATCH:
		patchType, ok := kubernetesPatchType(request.GetPatchType())
		if !ok {
			return invalidMutationResult(
				"UnsupportedPatchType",
				"requested Kubernetes patch type is unsupported",
			), nil
		}
		if patchType == types.ApplyPatchType {
			if _, decodeErr := decodeMutationObject(
				request,
				body,
				true,
			); decodeErr != nil {
				return invalidMutationResult(
					"InvalidApplyObject",
					"Kubernetes apply object identity does not match the request",
				), nil
			}
		}
		object, err = target.Patch(
			ctx,
			request.GetName(),
			patchType,
			body,
			patchOptions(request.GetMutationOptions()),
		)
	case agentv1.ResourceVerb_RESOURCE_VERB_DELETE:
		err = target.Delete(
			ctx,
			request.GetName(),
			deleteOptions(request.GetDeleteOptions()),
		)
		if err == nil {
			return resourceMutationResult{
				response: &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_OK,
					KubernetesStatusCode: http.StatusOK,
				},
			}, nil
		}
	default:
		return invalidMutationResult(
			"UnsupportedVerb",
			"requested Kubernetes resource verb is unsupported",
		), nil
	}
	if err != nil {
		return resourceMutationResult{
			response: kubernetesResourceError(err),
		}, nil
	}
	response, responseBody, marshalErr := marshalResourceObject(
		object,
		statusCode,
		maxBodyBytes,
	)
	if marshalErr != nil {
		return resourceMutationResult{}, marshalErr
	}
	return resourceMutationResult{response: response, body: responseBody}, nil
}

func decodeMutationObject(
	request *agentv1.ResourceRequest,
	body []byte,
	nameRequired bool,
) (*unstructured.Unstructured, error) {
	object := &unstructured.Unstructured{}
	if err := object.UnmarshalJSON(body); err != nil {
		return nil, err
	}
	resource := request.GetResource()
	gvk := object.GroupVersionKind()
	if object.GetKind() == "" ||
		gvk.Group != resource.GetGroup() ||
		gvk.Version != resource.GetVersion() ||
		object.GetNamespace() != request.GetNamespace() ||
		object.GetGenerateName() != "" {
		return nil, errors.New("Kubernetes resource identity mismatch")
	}
	if nameRequired {
		if object.GetName() != request.GetName() {
			return nil, errors.New("Kubernetes resource name mismatch")
		}
	} else if object.GetName() == "" {
		return nil, errors.New("Kubernetes resource name is required")
	}
	return object, nil
}

func createOptions(options *agentv1.MutationOptions) metav1.CreateOptions {
	result := metav1.CreateOptions{}
	if options == nil {
		return result
	}
	result.FieldManager = options.GetFieldManager()
	if options.GetDryRun() {
		result.DryRun = []string{metav1.DryRunAll}
	}
	return result
}

func updateOptions(options *agentv1.MutationOptions) metav1.UpdateOptions {
	result := metav1.UpdateOptions{}
	if options == nil {
		return result
	}
	result.FieldManager = options.GetFieldManager()
	if options.GetDryRun() {
		result.DryRun = []string{metav1.DryRunAll}
	}
	return result
}

func patchOptions(options *agentv1.MutationOptions) metav1.PatchOptions {
	result := metav1.PatchOptions{}
	if options == nil {
		return result
	}
	result.FieldManager = options.GetFieldManager()
	if options.GetDryRun() {
		result.DryRun = []string{metav1.DryRunAll}
	}
	if options.GetForce() {
		force := true
		result.Force = &force
	}
	return result
}

func deleteOptions(options *agentv1.DeleteOptions) metav1.DeleteOptions {
	result := metav1.DeleteOptions{}
	if options == nil {
		return result
	}
	if options.GetDryRun() {
		result.DryRun = []string{metav1.DryRunAll}
	}
	if options.GracePeriodSeconds != nil {
		value := options.GetGracePeriodSeconds()
		result.GracePeriodSeconds = &value
	}
	if propagation := kubernetesDeletePropagation(
		options.GetPropagation(),
	); propagation != nil {
		result.PropagationPolicy = propagation
	}
	if preconditions := options.GetPreconditions(); preconditions != nil {
		result.Preconditions = &metav1.Preconditions{}
		if preconditions.GetUid() != "" {
			uid := types.UID(preconditions.GetUid())
			result.Preconditions.UID = &uid
		}
		if preconditions.GetResourceVersion() != "" {
			value := preconditions.GetResourceVersion()
			result.Preconditions.ResourceVersion = &value
		}
	}
	return result
}

func kubernetesDeletePropagation(
	propagation agentv1.DeletePropagation,
) *metav1.DeletionPropagation {
	var value metav1.DeletionPropagation
	switch propagation {
	case agentv1.DeletePropagation_DELETE_PROPAGATION_ORPHAN:
		value = metav1.DeletePropagationOrphan
	case agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND:
		value = metav1.DeletePropagationBackground
	case agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND:
		value = metav1.DeletePropagationForeground
	default:
		return nil
	}
	return &value
}

func kubernetesPatchType(patchType agentv1.PatchType) (types.PatchType, bool) {
	switch patchType {
	case agentv1.PatchType_PATCH_TYPE_JSON:
		return types.JSONPatchType, true
	case agentv1.PatchType_PATCH_TYPE_MERGE:
		return types.MergePatchType, true
	case agentv1.PatchType_PATCH_TYPE_STRATEGIC_MERGE:
		return types.StrategicMergePatchType, true
	case agentv1.PatchType_PATCH_TYPE_APPLY:
		return types.ApplyPatchType, true
	default:
		return "", false
	}
}

func invalidMutationResult(
	reason string,
	message string,
) resourceMutationResult {
	return resourceMutationResult{
		response: resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			reason,
			message,
		),
	}
}

func marshalResourceObject(
	object any,
	statusCode int,
	maxBodyBytes uint64,
) (*agentv1.ResourceResponse, []byte, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, nil, errors.New("marshal Kubernetes resource response")
	}
	if uint64(len(body)) > maxBodyBytes {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
			http.StatusRequestEntityTooLarge,
			"ResponseTooLarge",
			"Kubernetes resource response exceeds the configured limit",
		), nil, nil
	}
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: int32(statusCode),
		ContentType:          kubernetesJSONContentType,
		BodySize:             uint64(len(body)),
	}, body, nil
}

func kubernetesResourceError(err error) *agentv1.ResourceResponse {
	if errors.Is(err, context.Canceled) {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_CANCELED,
			0,
			"Canceled",
			"Kubernetes API request was canceled",
		)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return resourceErrorResponse(
			agentv1.ResultCode_RESULT_CODE_TIMEOUT,
			http.StatusGatewayTimeout,
			"Timeout",
			"Kubernetes API request timed out",
		)
	}

	result := agentv1.ResultCode_RESULT_CODE_INTERNAL
	statusCode := int32(http.StatusInternalServerError)
	switch {
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err),
		apierrors.IsMethodNotSupported(err):
		result = agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT
	case apierrors.IsUnauthorized(err):
		result = agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED
	case apierrors.IsForbidden(err):
		result = agentv1.ResultCode_RESULT_CODE_FORBIDDEN
	case apierrors.IsNotFound(err), apierrors.IsGone(err):
		result = agentv1.ResultCode_RESULT_CODE_NOT_FOUND
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		result = agentv1.ResultCode_RESULT_CODE_CONFLICT
	case apierrors.IsTooManyRequests(err):
		result = agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
	case apierrors.IsServerTimeout(err), apierrors.IsTimeout(err):
		result = agentv1.ResultCode_RESULT_CODE_TIMEOUT
	case apierrors.IsServiceUnavailable(err):
		result = agentv1.ResultCode_RESULT_CODE_UNAVAILABLE
	}

	reason := string(apierrors.ReasonForError(err))
	if reason == "" {
		reason = "KubernetesAPIError"
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code > 0 {
		statusCode = status.Status().Code
	}
	return resourceErrorResponse(
		result,
		statusCode,
		reason,
		"Kubernetes API request failed",
	)
}

func resourceErrorResponse(
	result agentv1.ResultCode,
	statusCode int32,
	reason string,
	message string,
) *agentv1.ResourceResponse {
	return &agentv1.ResourceResponse{
		Result:               result,
		KubernetesStatusCode: statusCode,
		Reason:               reason,
		Message:              message,
	}
}
