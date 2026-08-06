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
	"sync"
	"time"

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

const (
	kubernetesResourceDiscoveryTTL        = 30 * time.Second
	maxKubernetesResourceDiscoveryEntries = 1024
)

type discoveredKubernetesResource struct {
	namespaced bool
	verbs      map[string]struct{}
	found      bool
	expiresAt  time.Time
}

type kubernetesResourceDiscoveryPolicy struct {
	client  discovery.DiscoveryInterface
	mutex   sync.Mutex
	entries map[schema.GroupVersionResource]discoveredKubernetesResource
}

// newKubernetesResourceHandler adapts the generic Resource Stream contract to
// client-go's dynamic client. The handler intentionally returns Kubernetes
// failures as ResourceResponse values: only malformed Stream traffic resets a
// Stream, while an API authorization or lookup failure is a normal business
// response.
func newKubernetesResourceHandler(
	client dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	maxBodyBytes uint64,
	identityNamespace string,
) func(
	context.Context,
	*agentv1.ResourceRequest,
	io.Reader,
) (*agentv1.ResourceResponse, io.Reader, error) {
	if maxBodyBytes == 0 {
		maxBodyBytes = agentprotocol.DefaultMaxResourceBodySize
	}
	mutationCache := newResourceMutationCache()
	discoveryPolicy := &kubernetesResourceDiscoveryPolicy{
		client:  discoveryClient,
		entries: make(map[schema.GroupVersionResource]discoveredKubernetesResource),
	}
	return func(
		ctx context.Context,
		request *agentv1.ResourceRequest,
		requestBody io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
			return discoverKubernetesResources(
				ctx,
				client,
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
		if !allowedKubernetesResourceRequest(request, identityNamespace) {
			return resourceErrorResponse(
				agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
				http.StatusForbidden,
				"ResourceNotAllowed",
				"requested Kubernetes resource is not enabled for this Agent",
			), nil, nil
		}
		if discoveryClient != nil {
			allowed, invalidScope, err := discoveryPolicy.allowed(ctx, request)
			if err != nil {
				return resourceErrorResponse(
					agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
					http.StatusServiceUnavailable,
					"DiscoveryFailed",
					"Kubernetes API discovery failed",
				), nil, nil
			}
			if invalidScope {
				return resourceErrorResponse(
					agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
					http.StatusBadRequest,
					"InvalidResourceScope",
					"Kubernetes resource Namespace does not match its scope",
				), nil, nil
			}
			if !allowed {
				return resourceErrorResponse(
					agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
					http.StatusForbidden,
					"ResourceNotAllowed",
					"requested Kubernetes resource is not enabled for this Agent",
				), nil, nil
			}
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

func (policy *kubernetesResourceDiscoveryPolicy) allowed(
	ctx context.Context,
	request *agentv1.ResourceRequest,
) (bool, bool, error) {
	resource := request.GetResource()
	gvr := schema.GroupVersionResource{
		Group: resource.GetGroup(), Version: resource.GetVersion(),
		Resource: resource.GetResource(),
	}
	discovered, err := policy.resource(ctx, gvr)
	if err != nil {
		return false, false, err
	}
	if !discovered.found {
		return false, false, nil
	}
	if _, supported := discovered.verbs[resourceVerbName(request.GetVerb())]; !supported {
		return false, false, nil
	}
	namespace := request.GetNamespace()
	if discovered.namespaced {
		if namespace == "" &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_LIST {
			return false, true, nil
		}
	} else if namespace != "" {
		return false, true, nil
	}
	return true, false, nil
}

func (policy *kubernetesResourceDiscoveryPolicy) resource(
	ctx context.Context,
	gvr schema.GroupVersionResource,
) (discoveredKubernetesResource, error) {
	now := time.Now()
	policy.mutex.Lock()
	if cached, exists := policy.entries[gvr]; exists &&
		cached.expiresAt.After(now) {
		policy.mutex.Unlock()
		return cached, nil
	}
	policy.mutex.Unlock()

	groupVersion := gvr.Version
	if gvr.Group != "" {
		groupVersion = gvr.Group + "/" + gvr.Version
	}
	list, err := policy.client.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		return discoveredKubernetesResource{}, err
	}
	if list == nil {
		return discoveredKubernetesResource{}, errors.New(
			"Kubernetes API discovery returned an empty resource list",
		)
	}
	result := discoveredKubernetesResource{
		verbs:     make(map[string]struct{}),
		expiresAt: now.Add(kubernetesResourceDiscoveryTTL),
	}
	for _, candidate := range list.APIResources {
		if candidate.Name != gvr.Resource {
			continue
		}
		result.found = true
		result.namespaced = candidate.Namespaced
		for _, verb := range supportedKubernetesVerbs(candidate.Verbs) {
			result.verbs[verb] = struct{}{}
		}
		break
	}
	policy.remember(gvr, result)
	return result, nil
}

func (policy *kubernetesResourceDiscoveryPolicy) remember(
	gvr schema.GroupVersionResource,
	resource discoveredKubernetesResource,
) {
	policy.mutex.Lock()
	defer policy.mutex.Unlock()
	if _, exists := policy.entries[gvr]; !exists &&
		len(policy.entries) >= maxKubernetesResourceDiscoveryEntries {
		var oldestGVR schema.GroupVersionResource
		var oldestExpiry time.Time
		for candidate, cached := range policy.entries {
			if oldestExpiry.IsZero() || cached.expiresAt.Before(oldestExpiry) {
				oldestGVR = candidate
				oldestExpiry = cached.expiresAt
			}
		}
		delete(policy.entries, oldestGVR)
	}
	policy.entries[gvr] = resource
}

func resourceVerbName(verb agentv1.ResourceVerb) string {
	switch verb {
	case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
		return "list"
	case agentv1.ResourceVerb_RESOURCE_VERB_GET:
		return "get"
	case agentv1.ResourceVerb_RESOURCE_VERB_CREATE:
		return "create"
	case agentv1.ResourceVerb_RESOURCE_VERB_UPDATE:
		return "update"
	case agentv1.ResourceVerb_RESOURCE_VERB_PATCH:
		return "patch"
	case agentv1.ResourceVerb_RESOURCE_VERB_DELETE:
		return "delete"
	default:
		return ""
	}
}

// allowedKubernetesResourceRequest is deliberately narrower than arbitrary
// Kubernetes API access. Phase 2 permits CRUD only on primary resources, with
// Secrets reachable solely through the Server's dedicated Secret API and never
// in this Agent's own namespace; Discovery and the Agent ServiceAccount RBAC
// remain the resource-specific authorization boundaries.
func allowedKubernetesResourceRequest(
	request *agentv1.ResourceRequest,
	identityNamespace string,
) bool {
	resource := request.GetResource()
	return resource != nil &&
		request.GetSubresource() == "" &&
		allowedKubernetesResource(request, identityNamespace) &&
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

/*
 * Kept out of the Discovery catalog.
 *
 * Neither is manageable through the generic resource API, and a catalog entry
 * for one would be an entry whose every request is refused. Secrets are
 * managed through their own API, which does not read this catalog.
 */
func hiddenFromKubernetesCatalog(group string, resource string) bool {
	return group == "" && (resource == "secrets" || resource == "events")
}

/*
 * Whether this Agent will act on the resource at all.
 *
 * Two core resources are refused here as well as by the Server, so a Server
 * that asked for them — through a bug, or because it had been made to — still
 * gets nothing. Events have their own stream and never travel this one.
 *
 * Secrets are the exception, and only under the flag the Server's dedicated
 * Secret API sets: it is checked here rather than trusted from the Server
 * alone, which keeps the generic resource and YAML APIs unable to reach a
 * Secret no matter what they send.
 */
func allowedKubernetesResource(request *agentv1.ResourceRequest, identityNamespace string) bool {
	resource := request.GetResource()
	group, name := resource.GetGroup(), resource.GetResource()
	if group != "" {
		return true
	}
	switch name {
	case "events":
		return false
	case "secrets":
		// Never this Agent's own namespace, whatever the Server asks and
		// whatever the ServiceAccount is allowed. That namespace holds the
		// Agent's identity key, its enrollment token and the certificates it
		// trusts the Server by; reading them is impersonating this Agent, and
		// no Console page has any business doing it. A list is refused rather
		// than filtered, because a Secret list of one namespace is a request
		// for that namespace.
		return request.GetSecretAccess() &&
			resource.GetVersion() == "v1" &&
			request.GetNamespace() != "" &&
			request.GetNamespace() != identityNamespace
	default:
		return true
	}
}

var customResourceDefinitionResource = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

const (
	customResourceDefinitionPageSize = 256
	maxCustomResourceDefinitionPages = 16
)

func customResourceKey(group string, resource string) string {
	return group + "/" + resource
}

// customResourceDefinitionNames answers which discovered resources are served
// by a CustomResourceDefinition.
//
// Kubernetes API discovery does not carry that fact — a CRD-backed resource
// looks exactly like a built-in one — so it has to be read from the CRD objects
// themselves. The second return value is whether the answer is known at all:
// the read needs its own RBAC and may be refused, and a browser that filtered
// on "custom" using a silently empty set would show an empty list and call it a
// cluster without CRDs.
func customResourceDefinitionNames(
	ctx context.Context,
	client dynamic.Interface,
) (map[string]struct{}, bool) {
	if client == nil {
		return nil, false
	}
	names := make(map[string]struct{})
	continueToken := ""
	for page := 0; page < maxCustomResourceDefinitionPages; page++ {
		list, err := client.Resource(customResourceDefinitionResource).List(ctx, metav1.ListOptions{
			Limit:    customResourceDefinitionPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return nil, false
		}
		for index := range list.Items {
			group, _, _ := unstructured.NestedString(list.Items[index].Object, "spec", "group")
			plural, _, _ := unstructured.NestedString(list.Items[index].Object, "spec", "names", "plural")
			if group == "" || plural == "" {
				continue
			}
			names[customResourceKey(group, plural)] = struct{}{}
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return names, true
		}
	}
	// More CRDs than this will read. Reporting what was collected would mark the
	// unread remainder as built-in, so the whole answer is withheld instead.
	return nil, false
}

func discoverKubernetesResources(
	ctx context.Context,
	dynamicClient dynamic.Interface,
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

	customResources, customResourcesKnown := customResourceDefinitionNames(ctx, dynamicClient)
	catalog := kubernetescatalog.Catalog{
		Resources:            make([]kubernetescatalog.Resource, 0),
		Partial:              discoveryErr != nil,
		CustomResourcesKnown: customResourcesKnown,
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
			if hiddenFromKubernetesCatalog(group, apiResource.Name) {
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
			_, customResource := customResources[customResourceKey(group, apiResource.Name)]
			catalog.Resources = append(
				catalog.Resources,
				kubernetescatalog.Resource{
					Group:          group,
					Version:        version,
					Resource:       apiResource.Name,
					Kind:           apiResource.Kind,
					Namespaced:     apiResource.Namespaced,
					Verbs:          verbs,
					ShortNames:     append([]string(nil), apiResource.ShortNames...),
					Categories:     append([]string(nil), apiResource.Categories...),
					CustomResource: customResource,
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

// mutationDryRun reports whether the request asks Kubernetes to validate the
// mutation without applying it. Delete carries the flag in its own options
// message, every other verb in MutationOptions.
func mutationDryRun(request *agentv1.ResourceRequest) bool {
	if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
		return request.GetDeleteOptions().GetDryRun()
	}
	return request.GetMutationOptions().GetDryRun()
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
	// A DryRun asks Kubernetes to validate and discard, so no outcome of one is
	// applied and none reserves its idempotency key. A preflight is normally
	// repeated with corrected content under the same key, and even an unchanged
	// repeat deserves a fresh answer: the state it collided with — a name already
	// taken, a quota already full — is exactly what may have been resolved since.
	dryRun := mutationDryRun(request)
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
				applied: !dryRun,
			}, nil
		}
	default:
		return invalidMutationResult(
			"UnsupportedVerb",
			"requested Kubernetes resource verb is unsupported",
		), nil
	}
	if err != nil {
		response := kubernetesResourceError(err)
		return resourceMutationResult{
			response: response,
			applied: !dryRun &&
				mutationFailureApplied(response.GetKubernetesStatusCode()),
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
	// Applied even when marshalResourceObject refused to ship the object back for
	// being too large: Kubernetes had already accepted the mutation by then, and
	// the key has to stay reserved so a retry does not perform it twice.
	return resourceMutationResult{
		response: response,
		body:     responseBody,
		applied:  !dryRun,
	}, nil
}

// mutationFailureApplied reports whether a failed mutation may still have
// changed cluster state, judged by the status Kubernetes answered with.
//
// A 4xx is the API Server refusing the request — invalid object, name taken,
// stale resourceVersion, no permission — and nothing was written. Everything
// else is an outcome this Agent cannot account for: a 5xx or a timeout may have
// committed before the connection failed, and a canceled call reports no status
// at all. Those keep their idempotency key, which is the case the replay cache
// was built for.
func mutationFailureApplied(status int32) bool {
	return status < http.StatusBadRequest || status >= http.StatusInternalServerError
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

// invalidMutationResult refuses a mutation before it reaches Kubernetes, so the
// result is not applied and does not reserve the idempotency key.
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
		kubernetesRejectionMessage(err),
	)
}

// The longest Kubernetes explanation worth carrying back to the caller. A
// rejection listing every invalid field of a large object has said what it needs
// to say well before this.
const maxKubernetesRejectionMessage = 1024

// kubernetesRejectionMessage is what the caller is told about a failed request.
//
// For `Invalid` — the API Server refusing the very object this caller submitted
// — it is the API Server's own explanation, because that explanation is about
// the submitted fields and is often the only place the reason appears at all: a
// NodePort outside the cluster's configured range is rejected with the range in
// the message, and nothing else in the request or the object says what that
// range is. Every other failure keeps a fixed message; those are about the
// cluster rather than the request, and their text is not the caller's business.
func kubernetesRejectionMessage(err error) string {
	if !apierrors.IsInvalid(err) {
		return "Kubernetes API request failed"
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return "Kubernetes API request failed"
	}
	message := strings.TrimSpace(status.Status().Message)
	if message == "" {
		return "Kubernetes API request failed"
	}
	if len(message) > maxKubernetesRejectionMessage {
		return message[:maxKubernetesRejectionMessage]
	}
	return message
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
