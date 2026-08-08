package kubernetesdescribe

import (
	"context"
	"sort"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type ServiceInput struct {
	ClusterID string
	Namespace string
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
