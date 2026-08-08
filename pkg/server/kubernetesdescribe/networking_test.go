package kubernetesdescribe

import (
	"context"
	"errors"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func testServiceDetail() kubernetesresource.NetworkingResourceDetail {
	return kubernetesresource.NetworkingResourceDetail{
		NetworkingResourceSummary: kubernetesresource.NetworkingResourceSummary{
			Resource:   kubernetesresource.NetworkingServices,
			APIVersion: "v1", Kind: "Service", Namespace: "models",
			Name: "inference", UID: "service-uid", ResourceVersion: "52",
			Service: &kubernetesresource.ServiceView{
				Spec: kubernetesresource.ServiceSpec{
					Type: "ClusterIP", Selector: map[string]string{"app": "inference"},
				},
			},
		},
	}
}

func testIngressDetail() kubernetesresource.NetworkingResourceDetail {
	return kubernetesresource.NetworkingResourceDetail{
		NetworkingResourceSummary: kubernetesresource.NetworkingResourceSummary{
			Resource:   kubernetesresource.NetworkingIngresses,
			APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "models",
			Name: "inference", UID: "ingress-uid", ResourceVersion: "61",
			Ingress: &kubernetesresource.IngressView{Spec: kubernetesresource.IngressSpec{
				IngressClassName: "nginx",
				Rules: []kubernetesresource.IngressRule{{
					Host: "models.example.com",
					Paths: []kubernetesresource.IngressPath{{
						Path: "/v1", PathType: "Prefix",
						Backend: kubernetesresource.IngressServiceBackend{Name: "inference-api", PortNumber: 80},
					}},
				}},
			}},
		},
	}
}

func testGatewayDetail() kubernetesresource.NetworkingResourceDetail {
	return kubernetesresource.NetworkingResourceDetail{
		NetworkingResourceSummary: kubernetesresource.NetworkingResourceSummary{
			Resource:   kubernetesresource.NetworkingGateways,
			APIVersion: "gateway.networking.k8s.io/v1", Kind: "Gateway", Namespace: "models",
			Name: "public", UID: "gateway-uid", ResourceVersion: "71",
			Gateway: &kubernetesresource.GatewayView{
				Conditions: []kubernetesresource.ResourceCondition{{
					Type: "Accepted", Status: "False", Reason: "Invalid",
					Message: "GatewayClass does not accept this Gateway",
				}},
				Listeners: []kubernetesresource.GatewayListenerStatus{{
					Name: "https", AttachedRoutes: 0,
					Conditions: []kubernetesresource.ResourceCondition{{
						Type: "ResolvedRefs", Status: "False", Reason: "InvalidCertificateRef",
						Message: "certificate reference is invalid",
					}},
				}},
			},
		},
	}
}

func TestDescribeGatewayUsesGatewayAndListenerConditions(t *testing.T) {
	t.Parallel()

	result, err := NewService(
		&fakeResourceAccess{networking: testGatewayDetail()},
		&fakeEventSource{}, Config{},
	).DescribeGateway(context.Background(), GatewayInput{
		ClusterID: testClusterID, Namespace: "models", Name: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GatewayStatus == nil || len(result.GatewayStatus.Listeners) != 1 {
		t.Fatalf("missing Gateway listener diagnosis: %+v", result)
	}
	if !hasFindingCode(result.Findings, FindingGatewayAddressPending) ||
		!hasFindingCode(result.Findings, FindingGatewayNotAccepted) {
		t.Fatalf("missing Gateway findings: %+v", result.Findings)
	}
	listener := result.GatewayStatus.Listeners[0]
	if listener.Name != "https" ||
		!hasFindingCode(listener.Findings, FindingGatewayListenerReferencesInvalid) {
		t.Fatalf("missing listener finding: %+v", listener)
	}
	if listener.Findings[0].Reason != "InvalidCertificateRef" ||
		listener.Findings[0].Message != "certificate reference is invalid" {
		t.Fatalf("listener condition evidence was not preserved: %+v", listener.Findings)
	}
}

func TestDescribeIngressJoinsBackendServicesAndEndpointSlices(t *testing.T) {
	t.Parallel()

	ready := false
	portName, portNumber := "http", int32(8080)
	slice := discoveryv1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "inference-api-a", Namespace: "models",
			Labels: map[string]string{discoveryv1.LabelServiceName: "inference-api"},
		},
		Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &portNumber}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.51"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	}
	item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&slice)
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeResourceAccess{
		networking: testIngressDetail(),
		networkingPage: kubernetesresource.NetworkingResourcePage{
			Resources: []kubernetesresource.NetworkingResourceSummary{{
				Resource:   kubernetesresource.NetworkingServices,
				APIVersion: "v1", Kind: "Service", Namespace: "models", Name: "inference-api",
				Service: &kubernetesresource.ServiceView{Spec: kubernetesresource.ServiceSpec{
					Ports: []kubernetesresource.ServicePort{{Name: "http", Port: 80}},
				}},
			}},
		},
		lists: map[string]kubernetesresource.ResourcePage{
			"endpointslices": {Items: []map[string]any{item}},
		},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeIngress(
		context.Background(),
		IngressInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.IngressBackends == nil || len(result.IngressBackends.Items) != 1 {
		t.Fatalf("missing Ingress backend status: %+v", result)
	}
	backend := result.IngressBackends.Items[0]
	if backend.ServiceFound == nil || !*backend.ServiceFound ||
		backend.PortFound == nil || !*backend.PortFound ||
		backend.Endpoints != 1 || backend.ReadyEndpoints != 0 ||
		!hasFindingCode(backend.Findings, FindingIngressBackendNoReadyEndpoints) {
		t.Fatalf("unexpected backend diagnosis: %+v", backend)
	}
	if access.networkingListInput.Resource != kubernetesresource.NetworkingServices ||
		access.networkingListInput.Limit != kubernetesresource.MaxResourceListLimit ||
		access.listInput["endpointslices"].LabelSelector != "kubernetes.io/service-name in (inference-api)" {
		t.Fatalf("unexpected inventory queries: services=%+v slices=%+v",
			access.networkingListInput, access.listInput["endpointslices"])
	}
	if !hasFindingCode(result.Findings, FindingIngressAddressPending) {
		t.Fatalf("missing Ingress address finding: %+v", result.Findings)
	}
}

func TestDescribeIngressOnlyReportsMissingServiceFromCompleteInventory(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{networking: testIngressDetail()}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeIngress(
		context.Background(),
		IngressInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.IngressBackends == nil ||
		!hasFindingCode(result.IngressBackends.Items[0].Findings, FindingIngressBackendServiceNotFound) {
		t.Fatalf("complete inventory did not report missing Service: %+v", result.IngressBackends)
	}

	access.networkingPage.ContinueToken = "next-page"
	result, err = NewService(access, &fakeEventSource{}, Config{}).DescribeIngress(
		context.Background(),
		IngressInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	backend := result.IngressBackends.Items[0]
	if backend.ServiceFound != nil || len(backend.Findings) != 0 {
		t.Fatalf("truncated inventory produced a definitive missing-Service finding: %+v", backend)
	}
}

func TestDescribeIngressReportsMissingServicePort(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		networking: testIngressDetail(),
		networkingPage: kubernetesresource.NetworkingResourcePage{
			Resources: []kubernetesresource.NetworkingResourceSummary{{
				Resource:  kubernetesresource.NetworkingServices,
				Namespace: "models", Name: "inference-api",
				Service: &kubernetesresource.ServiceView{Spec: kubernetesresource.ServiceSpec{
					Ports: []kubernetesresource.ServicePort{{Name: "https", Port: 443}},
				}},
			}},
		},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeIngress(
		context.Background(),
		IngressInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	backend := result.IngressBackends.Items[0]
	if backend.PortFound == nil || *backend.PortFound ||
		!hasFindingCode(backend.Findings, FindingIngressBackendPortNotFound) {
		t.Fatalf("missing backend-port finding: %+v", backend)
	}
	if _, called := access.listInput["endpointslices"]; called {
		t.Fatal("EndpointSlices were listed for a backend whose Service port does not exist")
	}
}

func TestIngressFindingsKeepControllerRejectionEvidence(t *testing.T) {
	t.Parallel()

	findings := ingressFindings(testIngressDetail(), []Event{{
		UID: "event-rejected", Type: "Warning", Reason: "Rejected",
		Message: "annotation contains an invalid value",
	}})
	if !hasFindingCode(findings, FindingIngressAddressPending) ||
		!hasFindingCode(findings, FindingIngressControllerRejected) {
		t.Fatalf("missing Ingress findings: %+v", findings)
	}
	var rejected Finding
	for _, finding := range findings {
		if finding.Code == FindingIngressControllerRejected {
			rejected = finding
		}
	}
	if rejected.Reason != "Rejected" || rejected.Message != "annotation contains an invalid value" ||
		len(rejected.Evidence) != 1 || rejected.Evidence[0].Name != "event-rejected" {
		t.Fatalf("controller evidence was not preserved: %+v", rejected)
	}
}

func TestDescribeSelectorlessServiceUsesManualEndpointSlices(t *testing.T) {
	t.Parallel()

	ready := true
	slice := discoveryv1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "inference-manual", Namespace: "models",
			Labels: map[string]string{discoveryv1.LabelServiceName: "inference"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.40"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	}
	item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&slice)
	if err != nil {
		t.Fatal(err)
	}
	detail := testServiceDetail()
	detail.Service.Spec.Selector = map[string]string{}
	access := &fakeResourceAccess{
		networking: detail,
		podListErr: errors.New("Pod list must not be called for selectorless Service"),
		lists: map[string]kubernetesresource.ResourcePage{
			"endpointslices": {Items: []map[string]any{item}},
		},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeService(
		context.Background(),
		ServiceInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceEndpoints == nil || result.ServiceEndpoints.ReadyEndpoints != 1 ||
		hasFindingCode(result.Findings, FindingServiceNoEndpoints) ||
		containsString(result.DegradedSections, "related") {
		t.Fatalf("selectorless Service did not use its EndpointSlice: %+v", result)
	}
}

func TestDescribeServiceUsesEndpointSlicesAndShowsBackendPods(t *testing.T) {
	t.Parallel()

	ready := false
	slice := discoveryv1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "inference-a", Namespace: "models",
			Labels: map[string]string{discoveryv1.LabelServiceName: "inference"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.12"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	}
	item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&slice)
	if err != nil {
		t.Fatal(err)
	}
	pod := testPodDetail()
	pod.Namespace, pod.Name, pod.UID = "models", "inference-0", "pod-uid"
	access := &fakeResourceAccess{
		networking: testServiceDetail(),
		podDetails: []kubernetesresource.PodDetail{pod},
		lists: map[string]kubernetesresource.ResourcePage{
			"endpointslices": {Items: []map[string]any{item}},
		},
	}

	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeService(
		context.Background(),
		ServiceInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Family != FamilyNetworking || result.Networking == nil ||
		result.ServiceEndpoints == nil || result.ServiceEndpoints.Endpoints != 1 ||
		result.ServiceEndpoints.ReadyEndpoints != 0 {
		t.Fatalf("unexpected Service projection: %+v", result)
	}
	if !hasFindingCode(result.Findings, FindingServiceNoReadyEndpoints) {
		t.Fatalf("missing no-ready-endpoints finding: %+v", result.Findings)
	}
	if access.listInput["endpointslices"].LabelSelector != "kubernetes.io/service-name=inference" ||
		access.podListInput.LabelSelector != "app=inference" {
		t.Fatalf("unexpected backend selectors: slice=%+v pod=%+v",
			access.listInput["endpointslices"], access.podListInput)
	}
	if len(result.Related.Pods) != 1 || result.Related.Pods[0].Name != "inference-0" {
		t.Fatalf("backend Pod missing: %+v", result.Related)
	}
}

func TestDescribeExternalNameServiceDoesNotExpectEndpoints(t *testing.T) {
	t.Parallel()

	service := testServiceDetail()
	service.Service.Spec.Type = "ExternalName"
	service.Service.Spec.ExternalName = "database.example.com"
	service.Service.Spec.Selector = map[string]string{}
	access := &fakeResourceAccess{networking: service}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeService(
		context.Background(),
		ServiceInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 || result.ServiceEndpoints == nil {
		t.Fatalf("ExternalName was diagnosed as missing endpoints: %+v", result)
	}
	if _, called := access.listInput["endpointslices"]; called {
		t.Fatal("ExternalName triggered an EndpointSlice list")
	}
}

func TestDescribeServiceReportsMissingEndpointsAndPendingLoadBalancer(t *testing.T) {
	t.Parallel()

	detail := testServiceDetail()
	detail.Service.Spec.Type = "LoadBalancer"
	access := &fakeResourceAccess{
		networking: detail,
		lists: map[string]kubernetesresource.ResourcePage{
			"endpointslices": {Items: []map[string]any{}},
		},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeService(
		context.Background(),
		ServiceInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFindingCode(result.Findings, FindingServiceNoEndpoints) ||
		!hasFindingCode(result.Findings, FindingServiceLoadBalancerPending) {
		t.Fatalf("missing Service findings: %+v", result.Findings)
	}
}

func TestDescribeServiceDoesNotConcludeNoEndpointsFromTruncatedList(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		networking: testServiceDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"endpointslices": {Items: []map[string]any{}, ContinueToken: "next-page"},
		},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeService(
		context.Background(),
		ServiceInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceEndpoints == nil || !result.ServiceEndpoints.Truncated {
		t.Fatalf("missing endpoint truncation: %+v", result.ServiceEndpoints)
	}
	if hasFindingCode(result.Findings, FindingServiceNoEndpoints) {
		t.Fatalf("truncated endpoint list produced a definitive finding: %+v", result.Findings)
	}
}
