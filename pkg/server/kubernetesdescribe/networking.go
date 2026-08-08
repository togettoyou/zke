package kubernetesdescribe

import (
	"context"
	"sort"
	"strconv"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
)

const maxIngressBackends = 20

type ServiceInput struct {
	ClusterID string
	Namespace string
	Name      string
}

type IngressInput struct {
	ClusterID string
	Namespace string
	Name      string
}

type GatewayInput struct {
	ClusterID string
	Namespace string
	Name      string
}

type GatewayRouteInput struct {
	ClusterID string
	Namespace string
	Resource  kubernetesresource.NetworkingResource
	Name      string
}

var endpointSliceIdentity = kubernetesresource.ResourceIdentity{
	Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices",
}

// DescribeService joins the Service with EndpointSlice state and, when it has
// a selector, the Pods that should back it. EndpointSlice is authoritative:
// selectorless Services can legitimately be backed by manually managed slices.
func (service *Service) DescribeService(
	ctx context.Context,
	input ServiceInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	object, err := service.resources.GetNetworkingResource(
		ctx, input.ClusterID, input.Namespace,
		kubernetesresource.NetworkingServices, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if object.UID == "" || object.Service == nil {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion: object.APIVersion, Kind: object.Kind,
			Namespace: object.Namespace, Name: object.Name, UID: object.UID,
			ResourceVersion: object.ResourceVersion,
		},
		Family:     FamilyNetworking,
		Networking: &object,
		Related: &Related{
			Controllers: []RelatedObject{}, Pods: []RelatedObject{},
			PersistentVolumeClaims: []RelatedObject{},
		},
		Findings: []Finding{}, DegradedSections: []string{},
	}

	if object.Service.Spec.Type == "ExternalName" {
		result.ServiceEndpoints = &ServiceEndpoints{}
	} else {
		endpoints, endpointErr := service.serviceEndpoints(ctx, input)
		if endpointErr != nil {
			result.DegradedSections = append(result.DegradedSections, "service.endpoints")
		} else {
			result.ServiceEndpoints = &endpoints
		}
	}

	if len(object.Service.Spec.Selector) > 0 {
		pods, truncated, podErr := service.resources.ListPodDetails(
			ctx,
			kubernetesresource.ListPodsInput{
				ClusterID: input.ClusterID, Namespace: input.Namespace,
				Limit:         kubernetesresource.MaxResourceListLimit,
				LabelSelector: labels.SelectorFromSet(object.Service.Spec.Selector).String(),
			},
		)
		if podErr != nil {
			result.DegradedSections = append(result.DegradedSections, "related")
		} else {
			result.Related.Pods, result.Related.Truncated = servicePodObjects(pods, truncated)
		}
	}

	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	result.Findings = serviceFindings(object, result.ServiceEndpoints)
	return result, nil
}

// DescribeIngress joins an Ingress with the Services and EndpointSlices its
// service backends depend on. The inventory reads are bounded and shared by all
// backends rather than issuing two Agent round trips for every path.
func (service *Service) DescribeIngress(
	ctx context.Context,
	input IngressInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	object, err := service.resources.GetNetworkingResource(
		ctx, input.ClusterID, input.Namespace,
		kubernetesresource.NetworkingIngresses, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if object.UID == "" || object.Ingress == nil {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion: object.APIVersion, Kind: object.Kind,
			Namespace: object.Namespace, Name: object.Name, UID: object.UID,
			ResourceVersion: object.ResourceVersion,
		},
		Family: FamilyNetworking, Networking: &object,
		Findings: []Finding{}, DegradedSections: []string{},
	}
	backends := ingressBackendReferences(*object.Ingress)
	result.IngressBackends = &backends
	if len(backends.Items) > 0 {
		if err := service.populateIngressServices(ctx, input, &backends); err != nil {
			result.DegradedSections = append(result.DegradedSections, "ingress.backends")
		} else if err := service.populateIngressEndpoints(ctx, input, &backends); err != nil {
			result.DegradedSections = append(result.DegradedSections, "ingress.endpoints")
		}
	}
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	result.Findings = ingressFindings(object, result.Events.Items)
	populateIngressBackendFindings(&backends)
	return result, nil
}

// DescribeGateway keeps Gateway API's own Condition vocabulary intact. The
// controller already reports acceptance, programming and listener reference
// failures, so no related-object fanout is needed to explain them.
func (service *Service) DescribeGateway(
	ctx context.Context,
	input GatewayInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	object, err := service.resources.GetNetworkingResource(
		ctx, input.ClusterID, input.Namespace,
		kubernetesresource.NetworkingGateways, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if object.UID == "" || object.Gateway == nil {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion: object.APIVersion, Kind: object.Kind,
			Namespace: object.Namespace, Name: object.Name, UID: object.UID,
			ResourceVersion: object.ResourceVersion,
		},
		Family: FamilyNetworking, Networking: &object,
		GatewayStatus: &GatewayStatus{Listeners: gatewayListenerDiagnostics(*object.Gateway)},
		Findings:      gatewayFindings(object), DegradedSections: []string{},
	}
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

// DescribeGatewayRoute reports the parent-specific Conditions written by the
// Gateway controller. They are authoritative for attachment and for backend
// reference authorization, including cross-namespace ReferenceGrant checks;
// ZKE deliberately does not widen the caller's Namespace by following those
// references itself.
func (service *Service) DescribeGatewayRoute(
	ctx context.Context,
	input GatewayRouteInput,
) (Result, error) {
	if service == nil || service.resources == nil ||
		!kubernetesresource.IsGatewayRouteResource(input.Resource) {
		return Result{}, ErrInvalidInput
	}
	object, err := service.resources.GetNetworkingResource(
		ctx, input.ClusterID, input.Namespace, input.Resource, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if object.UID == "" || object.GatewayRoute == nil {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{APIVersion: object.APIVersion, Kind: object.Kind,
			Namespace: object.Namespace, Name: object.Name, UID: object.UID,
			ResourceVersion: object.ResourceVersion},
		Family: FamilyNetworking, Networking: &object,
		Related: &Related{Controllers: []RelatedObject{}, Pods: []RelatedObject{},
			PersistentVolumeClaims: []RelatedObject{}},
		Findings: gatewayRouteFindings(object), DegradedSections: []string{},
	}
	for _, parent := range object.GatewayRoute.ParentRefs {
		namespace := parent.Namespace
		if namespace == "" {
			namespace = object.Namespace
		}
		kind := parent.Kind
		if kind == "" {
			kind = "Gateway"
		}
		result.Related.Controllers = append(result.Related.Controllers, RelatedObject{
			Kind: kind, Name: parent.Name, Namespace: namespace, Findings: []Finding{},
		})
	}
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

func gatewayListenerDiagnostics(view kubernetesresource.GatewayView) []GatewayListenerStatus {
	listeners := make([]GatewayListenerStatus, 0, len(view.Listeners))
	for _, listener := range view.Listeners {
		listeners = append(listeners, GatewayListenerStatus{
			Name: listener.Name, AttachedRoutes: listener.AttachedRoutes,
			Findings: gatewayListenerFindings(listener),
		})
	}
	return listeners
}

func ingressBackendReferences(view kubernetesresource.IngressView) IngressBackends {
	result := IngressBackends{Items: []IngressBackend{}}
	indexes := make(map[string]int)
	appendBackend := func(backend kubernetesresource.IngressServiceBackend, reference string) {
		// networking.k8s.io/v1 also permits a custom Resource backend. The
		// existing typed projection intentionally models only Service backends,
		// so an empty Service name is not reinterpreted as a missing Service.
		if backend.Name == "" {
			return
		}
		key := backend.Name + "\x00" + backend.PortName + "\x00" + strconv.FormatInt(int64(backend.PortNumber), 10)
		if index, found := indexes[key]; found {
			result.Items[index].References = append(result.Items[index].References, reference)
			return
		}
		if len(result.Items) >= maxIngressBackends {
			result.Truncated = true
			return
		}
		indexes[key] = len(result.Items)
		result.Items = append(result.Items, IngressBackend{
			ServiceName: backend.Name, PortName: backend.PortName, PortNumber: backend.PortNumber,
			References: []string{reference}, Findings: []Finding{},
		})
	}
	if view.Spec.DefaultBackend != nil {
		appendBackend(*view.Spec.DefaultBackend, "默认后端")
	}
	for _, rule := range view.Spec.Rules {
		host := rule.Host
		if host == "" {
			host = "*"
		}
		for _, path := range rule.Paths {
			value := path.Path
			if value == "" {
				value = "/"
			}
			appendBackend(path.Backend, host+value)
		}
	}
	return result
}

func (service *Service) populateIngressServices(
	ctx context.Context,
	input IngressInput,
	backends *IngressBackends,
) error {
	page, err := service.resources.ListNetworkingResources(
		ctx,
		kubernetesresource.ListNetworkingResourcesInput{
			ClusterID: input.ClusterID, Namespace: input.Namespace,
			Resource: kubernetesresource.NetworkingServices,
			Limit:    kubernetesresource.MaxResourceListLimit,
		},
	)
	if err != nil {
		return err
	}
	backends.ServicesTruncated = page.ContinueToken != ""
	services := make(map[string]kubernetesresource.ServiceView, len(page.Resources))
	for _, item := range page.Resources {
		if item.Resource != kubernetesresource.NetworkingServices ||
			item.Namespace != input.Namespace || item.Name == "" || item.Service == nil {
			return kubernetesresource.ErrInvalidResponse
		}
		services[item.Name] = *item.Service
	}
	for index := range backends.Items {
		backend := &backends.Items[index]
		view, found := services[backend.ServiceName]
		if !found {
			if !backends.ServicesTruncated {
				value := false
				backend.ServiceFound = &value
			}
			continue
		}
		value := true
		backend.ServiceFound = &value
		portFound, endpointPortName := ingressServicePort(view, *backend)
		backend.PortFound = &portFound
		backend.endpointPortName = endpointPortName
	}
	return nil
}

func (service *Service) populateIngressEndpoints(
	ctx context.Context,
	input IngressInput,
	backends *IngressBackends,
) error {
	names := make([]string, 0, len(backends.Items))
	seen := make(map[string]struct{})
	for index := range backends.Items {
		backend := &backends.Items[index]
		if backend.ServiceFound == nil || !*backend.ServiceFound ||
			backend.PortFound == nil || !*backend.PortFound {
			continue
		}
		if _, found := seen[backend.ServiceName]; !found {
			seen[backend.ServiceName] = struct{}{}
			names = append(names, backend.ServiceName)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	requirement, err := labels.NewRequirement(
		discoveryv1.LabelServiceName, selection.In, names,
	)
	if err != nil {
		return ErrInvalidInput
	}
	page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
		ClusterID: input.ClusterID, Resource: endpointSliceIdentity,
		Namespace: input.Namespace, Limit: kubernetesresource.MaxResourceListLimit,
		LabelSelector: labels.NewSelector().Add(*requirement).String(),
	})
	if err != nil {
		return err
	}
	backends.EndpointSlicesTruncated = page.ContinueToken != ""
	for _, item := range page.Items {
		var slice discoveryv1.EndpointSlice
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item, &slice); err != nil ||
			slice.Namespace != input.Namespace {
			return kubernetesresource.ErrInvalidResponse
		}
		serviceName := slice.Labels[discoveryv1.LabelServiceName]
		if _, found := seen[serviceName]; !found {
			return kubernetesresource.ErrInvalidResponse
		}
		for index := range backends.Items {
			backend := &backends.Items[index]
			if backend.ServiceName != serviceName || !endpointSliceHasIngressPort(slice, *backend) {
				continue
			}
			backend.EndpointSlices++
			for _, endpoint := range slice.Endpoints {
				backend.Endpoints++
				if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
					backend.ReadyEndpoints++
				}
			}
		}
	}
	for index := range backends.Items {
		backend := &backends.Items[index]
		if backend.ServiceFound != nil && *backend.ServiceFound &&
			backend.PortFound != nil && *backend.PortFound {
			backend.EndpointStateAvailable = true
		}
	}
	return nil
}

func ingressServicePort(
	service kubernetesresource.ServiceView,
	backend IngressBackend,
) (bool, string) {
	for _, port := range service.Spec.Ports {
		if backend.PortName != "" && port.Name == backend.PortName {
			return true, port.Name
		}
		if backend.PortName == "" && backend.PortNumber != 0 && port.Port == backend.PortNumber {
			return true, port.Name
		}
	}
	return false, ""
}

func endpointSliceHasIngressPort(slice discoveryv1.EndpointSlice, backend IngressBackend) bool {
	for _, port := range slice.Ports {
		name := ""
		if port.Name != nil {
			name = *port.Name
		}
		if name == backend.endpointPortName {
			return true
		}
	}
	return false
}

func (service *Service) serviceEndpoints(
	ctx context.Context,
	input ServiceInput,
) (ServiceEndpoints, error) {
	page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
		ClusterID: input.ClusterID, Resource: endpointSliceIdentity,
		Namespace: input.Namespace, Limit: kubernetesresource.MaxResourceListLimit,
		LabelSelector: labels.Set{
			discoveryv1.LabelServiceName: input.Name,
		}.AsSelector().String(),
	})
	if err != nil {
		return ServiceEndpoints{}, err
	}
	result := ServiceEndpoints{
		EndpointSlices: int64(len(page.Items)), Truncated: page.ContinueToken != "",
	}
	for _, item := range page.Items {
		var slice discoveryv1.EndpointSlice
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item, &slice); err != nil ||
			slice.Namespace != input.Namespace ||
			slice.Labels[discoveryv1.LabelServiceName] != input.Name {
			return ServiceEndpoints{}, kubernetesresource.ErrInvalidResponse
		}
		for _, endpoint := range slice.Endpoints {
			result.Endpoints++
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				result.ReadyEndpoints++
			}
			if endpoint.Conditions.Serving != nil && *endpoint.Conditions.Serving {
				result.ServingEndpoints++
			}
			if endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
				result.TerminatingEndpoints++
			}
		}
	}
	return result, nil
}

func servicePodObjects(
	pods []kubernetesresource.PodDetail,
	listTruncated bool,
) ([]RelatedObject, bool) {
	objects := make([]RelatedObject, 0, len(pods))
	for _, pod := range pods {
		findings := podFindings(pod, nil)
		objects = append(objects, RelatedObject{
			Kind: "Pod", Name: pod.Name, UID: pod.UID, Namespace: pod.Namespace,
			Status: podStatusText(pod), Ready: pod.Ready && len(findings) == 0,
			Findings: findings,
		})
	}
	sort.SliceStable(objects, func(left, right int) bool {
		return !objects[left].Ready && objects[right].Ready
	})
	truncated := listTruncated || len(objects) > maxRelatedPods
	if len(objects) > maxRelatedPods {
		objects = objects[:maxRelatedPods]
	}
	return objects, truncated
}
