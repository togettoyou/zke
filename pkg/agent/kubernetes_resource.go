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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	return func(
		ctx context.Context,
		request *agentv1.ResourceRequest,
		_ io.Reader,
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
		if request.GetRepresentation() !=
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
		(request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_LIST ||
			request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_GET)
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
			verbs := readableKubernetesVerbs(apiResource.Verbs)
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

func readableKubernetesVerbs(verbs metav1.Verbs) []string {
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
