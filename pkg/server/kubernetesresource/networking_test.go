package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestCreateNetworkingObjectsCoversServiceIngressAndGateway(t *testing.T) {
	t.Parallel()

	serviceObject, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingServices, Name: "api",
		Labels: map[string]string{"app": "api"}, Service: &ServiceSpec{
			Type: "ClusterIP", Headless: true, Selector: map[string]string{"app": "api"},
			Ports: []ServicePort{{Name: "http", Port: 80, TargetPort: "8080"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var service corev1.Service
	if runtime.DefaultUnstructuredConverter.FromUnstructured(serviceObject, &service) != nil ||
		service.Spec.ClusterIP != corev1.ClusterIPNone ||
		service.Spec.Ports[0].TargetPort.IntValue() != 8080 {
		t.Fatalf("unexpected Service object: %+v", service)
	}

	ingressObject, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingIngresses, Name: "api",
		Ingress: &IngressSpec{IngressClassName: "nginx", Rules: []IngressRule{{
			Host: "api.example.com", Paths: []IngressPath{{
				Path: "/", PathType: "Prefix",
				Backend: IngressServiceBackend{Name: "api", PortNumber: 80},
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ingress networkingv1.Ingress
	if runtime.DefaultUnstructuredConverter.FromUnstructured(ingressObject, &ingress) != nil ||
		ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != "api" {
		t.Fatalf("unexpected Ingress object: %+v", ingress)
	}

	gatewaySpec := &GatewaySpec{GatewayClassName: "example", Listeners: []GatewayListener{{
		Name: "https", Hostname: "api.example.com", Port: 443, Protocol: "HTTPS",
		TLS:           &GatewayTLS{Mode: "Terminate", CertificateRefs: []GatewayObjectReference{{Name: "api-tls"}}},
		AllowedRoutes: GatewayAllowedRoutes{NamespacesFrom: "Same", Kinds: []GatewayRouteKind{{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}}},
	}}}
	if !validGatewaySpec(*gatewaySpec) {
		t.Fatalf("Gateway test spec is invalid: tls=%t allowed_routes=%t", validGatewayTLS("HTTPS", gatewaySpec.Listeners[0].TLS), validGatewayAllowedRoutes(gatewaySpec.Listeners[0].AllowedRoutes))
	}
	gatewayObject, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingGateways, Name: "edge",
		Gateway: gatewaySpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	className, found, nestedErr := unstructured.NestedString(gatewayObject, "spec", "gatewayClassName")
	if nestedErr != nil || !found || className != "example" {
		t.Fatalf("Gateway class = %q found=%t err=%v object=%+v", className, found, nestedErr, gatewayObject)
	}
	listeners, found, nestedErr := unstructured.NestedSlice(gatewayObject, "spec", "listeners")
	if nestedErr != nil || !found || len(listeners) != 1 {
		t.Fatalf("Gateway listeners = %+v found=%t err=%v", listeners, found, nestedErr)
	}
	listener := listeners[0].(map[string]any)
	tls := listener["tls"].(map[string]any)
	refs := tls["certificateRefs"].([]any)
	if refs[0].(map[string]any)["name"] != "api-tls" {
		t.Fatalf("unexpected Gateway certificate refs: %+v", refs)
	}
}

func TestCreateExternalNameServiceWithoutPorts(t *testing.T) {
	t.Parallel()

	object, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingServices, Name: "external-api",
		Service: &ServiceSpec{Type: "ExternalName", ExternalName: "api.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var service corev1.Service
	if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &service) != nil ||
		service.Spec.Type != corev1.ServiceTypeExternalName || service.Spec.ExternalName != "api.example.com" ||
		len(service.Spec.Ports) != 0 {
		t.Fatalf("unexpected ExternalName Service: %+v", service)
	}
}

func TestNetworkingResourceDetailSummarizesStatus(t *testing.T) {
	t.Parallel()

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: types.UID("service-uid"), ResourceVersion: "7"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ClusterIPs: []string{"10.0.0.1"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}}}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(service)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := networkingResourceDetail(object, NetworkingServices, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if detail.UID != "service-uid" || detail.Service == nil ||
		detail.Service.Spec.Type != "LoadBalancer" ||
		detail.Service.LoadBalancerIngress[0].IP != "192.0.2.1" {
		t.Fatalf("unexpected Service detail: %+v", detail)
	}
}

func TestNetworkingResourceDetailUsesEmptyCollectionsInsteadOfNull(t *testing.T) {
	t.Parallel()

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(service)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := networkingResourceDetail(object, NetworkingServices, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, unexpected := range []string{`"labels":null`, `"annotations":null`, `"selector":null`, `"cluster_ips":null`, `"ip_families":null`, `"load_balancer_ingress":null`, `"allocate_load_balancer_node_ports":null`} {
		if strings.Contains(string(body), unexpected) {
			t.Fatalf("networking detail contains %s: %s", unexpected, body)
		}
	}
}

// An optional field the contract declares as absent-or-set must not arrive as
// `null`: a client typed from that contract reads the key being present as the
// field having a value, and a form built on that ticks its own "default
// backend" box on every open.
func TestIngressWithoutDefaultBackendOmitsTheField(t *testing.T) {
	t.Parallel()

	ingress := &networkingv1.Ingress{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "example.com"}}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ingress)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := networkingResourceDetail(object, NetworkingIngresses, "default", "web")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Ingress == nil || detail.Ingress.Spec.DefaultBackend != nil {
		t.Fatalf("unexpected Ingress detail: %+v", detail.Ingress)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"default_backend"`) {
		t.Fatalf("Ingress detail carries default_backend without one: %s", body)
	}
}

func TestNetworkingResourceDetailDecodesGatewayStatus(t *testing.T) {
	t.Parallel()

	object, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingGateways, Name: "edge",
		Gateway: &GatewaySpec{GatewayClassName: "example", Listeners: []GatewayListener{{
			Name: "http", Port: 80, Protocol: "HTTP",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := object["metadata"].(map[string]any)
	metadata["uid"] = "gateway-uid"
	metadata["resourceVersion"] = "9"
	object["status"] = map[string]any{
		"addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.10"}},
		"conditions": []any{map[string]any{
			"type": "Programmed", "status": "True", "reason": "Programmed",
			"message": "ready", "observedGeneration": int64(1),
			"lastTransitionTime": "2026-08-02T00:00:00Z",
		}},
		"listeners": []any{map[string]any{
			"name": "http", "attachedRoutes": int64(2), "conditions": []any{},
		}},
	}
	detail, err := networkingResourceDetail(object, NetworkingGateways, "default", "edge")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Gateway == nil || detail.Gateway.Addresses[0].Value != "192.0.2.10" ||
		detail.Gateway.Conditions[0].Type != "Programmed" ||
		detail.Gateway.Listeners[0].AttachedRoutes != 2 {
		t.Fatalf("unexpected Gateway detail: %+v", detail)
	}
}

func TestGatewayAPIUnavailableIsDistinctFromAccessDenied(t *testing.T) {
	t.Parallel()

	catalogBody, err := json.Marshal(kubernetescatalog.Catalog{Resources: []kubernetescatalog.Resource{{
		Version: "v1", Resource: "services", Kind: "Service", Namespaced: true, Verbs: []string{"get", "list"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
			t.Fatalf("unexpected request: %+v", request)
		}
		if _, err := responseBody.Write(catalogBody); err != nil {
			t.Fatal(err)
		}
		return &agentv1.ResourceResponse{
			Result: agentv1.ResultCode_RESULT_CODE_OK, KubernetesStatusCode: http.StatusOK,
			ContentType: kubernetesJSONContentType, BodySize: uint64(len(catalogBody)),
		}, nil
	}}
	_, err = NewService(requester).ListNetworkingResources(
		context.Background(),
		ListNetworkingResourcesInput{
			ClusterID: testClusterID, Namespace: "default", Resource: NetworkingGateways, Limit: 25,
		},
	)
	if !errors.Is(err, ErrGatewayAPIUnavailable) {
		t.Fatalf("error = %v, want Gateway API unavailable", err)
	}
}

func TestGatewayAPIAccessDeniedRemainsForbidden(t *testing.T) {
	t.Parallel()

	catalogBody, err := json.Marshal(kubernetescatalog.Catalog{Resources: []kubernetescatalog.Resource{{
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways", Kind: "Gateway",
		Namespaced: true, Verbs: []string{"get", "list", "create", "update", "patch", "delete"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
			if _, err := responseBody.Write(catalogBody); err != nil {
				t.Fatal(err)
			}
			return &agentv1.ResourceResponse{
				Result: agentv1.ResultCode_RESULT_CODE_OK, KubernetesStatusCode: http.StatusOK,
				ContentType: kubernetesJSONContentType, BodySize: uint64(len(catalogBody)),
			}, nil
		}
		return &agentv1.ResourceResponse{
			Result: agentv1.ResultCode_RESULT_CODE_FORBIDDEN, KubernetesStatusCode: http.StatusForbidden,
		}, nil
	}}
	_, err = NewService(requester).ListNetworkingResources(
		context.Background(),
		ListNetworkingResourcesInput{
			ClusterID: testClusterID, Namespace: "default", Resource: NetworkingGateways, Limit: 25,
		},
	)
	if !errors.Is(err, ErrClusterAccessDenied) {
		t.Fatalf("error = %v, want access denied", err)
	}
}

func TestUpdateNetworkingResourceRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: types.UID("current-uid"), ResourceVersion: "8"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, service), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateNetworkingResource(context.Background(), UpdateNetworkingResourceInput{
		ClusterID: testClusterID, Namespace: "default", Resource: NetworkingServices, Name: "api",
		UID: "stale-uid", ResourceVersion: "8", Confirm: true, IdempotencyKey: "network-update-0001",
		Service: &ServiceSpec{Type: "ClusterIP", Ports: []ServicePort{{Port: 80}}},
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestUpdateNetworkingObjectReplacesGatewaySpec(t *testing.T) {
	t.Parallel()

	existing, err := createNetworkingObject(CreateNetworkingResourceInput{
		Namespace: "default", Resource: NetworkingGateways, Name: "edge",
		Gateway: &GatewaySpec{GatewayClassName: "example", Listeners: []GatewayListener{{Name: "http", Port: 80, Protocol: "HTTP"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	existingSpec := existing["spec"].(map[string]any)
	existingSpec["infrastructure"] = map[string]any{"labels": map[string]any{"example.com/tier": "edge"}}
	updated, err := updateNetworkingObject(existing, UpdateNetworkingResourceInput{
		Resource: NetworkingGateways,
		Gateway:  &GatewaySpec{GatewayClassName: "example", Listeners: []GatewayListener{{Name: "https", Port: 443, Protocol: "HTTPS", TLS: &GatewayTLS{Mode: "Terminate", CertificateRefs: []GatewayObjectReference{{Name: "edge-tls"}}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	listeners, found, err := unstructured.NestedSlice(updated, "spec", "listeners")
	if err != nil || !found || listeners[0].(map[string]any)["name"] != "https" {
		t.Fatalf("updated Gateway listeners = %+v found=%t err=%v", listeners, found, err)
	}
	infrastructure, found, err := unstructured.NestedMap(updated, "spec", "infrastructure")
	if err != nil || !found || infrastructure["labels"] == nil {
		t.Fatalf("Gateway infrastructure was not preserved: %+v found=%t err=%v", infrastructure, found, err)
	}
}

func TestUpdateNetworkingObjectPreservesServiceFieldsOutsideTypedForm(t *testing.T) {
	t.Parallel()

	distribution := "PreferClose"
	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort, ClusterIP: "10.0.0.10", ClusterIPs: []string{"10.0.0.10"},
			ExternalIPs: []string{"192.0.2.10"}, TrafficDistribution: &distribution,
			Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, NodePort: 30080}},
		},
	}
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(service)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateNetworkingObject(existing, UpdateNetworkingResourceInput{
		Resource: NetworkingServices,
		Service: &ServiceSpec{Type: "NodePort", Ports: []ServicePort{{
			Name: "http", Protocol: "TCP", Port: 80, TargetPort: "8080",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result corev1.Service
	if runtime.DefaultUnstructuredConverter.FromUnstructured(updated, &result) != nil ||
		result.Spec.Ports[0].NodePort != 30080 || len(result.Spec.ExternalIPs) != 1 ||
		result.Spec.ExternalIPs[0] != "192.0.2.10" || pointerString(result.Spec.TrafficDistribution) != distribution {
		t.Fatalf("Service fields were not preserved: %+v", result.Spec)
	}
}

func TestUpdateNetworkingObjectRejectsHeadlessTransition(t *testing.T) {
	t.Parallel()

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.10", ClusterIPs: []string{"10.0.0.10"},
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(service)
	if err != nil {
		t.Fatal(err)
	}
	_, err = updateNetworkingObject(existing, UpdateNetworkingResourceInput{
		Resource: NetworkingServices,
		Service:  &ServiceSpec{Type: "ClusterIP", Headless: true, Ports: []ServicePort{{Port: 80}}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input", err)
	}
}
