package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

type NetworkingResource string

const (
	NetworkingServices  NetworkingResource = "services"
	NetworkingIngresses NetworkingResource = "ingresses"
	NetworkingGateways  NetworkingResource = "gateways"

	maxNetworkingPorts        = 100
	maxIngressRules           = 256
	maxIngressPaths           = 256
	maxGatewayListeners       = 64
	maxGatewayAddresses       = 16
	maxGatewayRouteKinds      = 64
	maxGatewayCertificateRefs = 64
	maxNetworkingAnnotations  = 256 * 1024
)

var ErrGatewayAPIUnavailable = errors.New("Gateway API v1 is not installed")

var networkingResourceIdentities = map[NetworkingResource]ResourceIdentity{
	NetworkingServices:  {Version: "v1", Resource: "services"},
	NetworkingIngresses: {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	NetworkingGateways:  {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
}

type ListNetworkingResourcesInput struct {
	ClusterID     string
	Namespace     string
	Resource      NetworkingResource
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type NetworkingResourcePage struct {
	Resources          []NetworkingResourceSummary `json:"resources"`
	ContinueToken      string                      `json:"continue_token"`
	ResourceVersion    string                      `json:"resource_version"`
	RemainingItemCount *int64                      `json:"remaining_item_count"`
}

type NetworkingResourceSummary struct {
	Resource          NetworkingResource `json:"resource"`
	APIVersion        string             `json:"api_version"`
	Kind              string             `json:"kind"`
	Namespace         string             `json:"namespace"`
	Name              string             `json:"name"`
	UID               string             `json:"uid"`
	ResourceVersion   string             `json:"resource_version"`
	CreationTimestamp time.Time          `json:"creation_timestamp"`
	Labels            map[string]string  `json:"labels"`
	Service           *ServiceView       `json:"service,omitempty"`
	Ingress           *IngressView       `json:"ingress,omitempty"`
	Gateway           *GatewayView       `json:"gateway,omitempty"`
}

type NetworkingResourceDetail struct {
	NetworkingResourceSummary
	Annotations map[string]string `json:"annotations"`
}

type ServicePort struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	AppProtocol string `json:"app_protocol"`
	Port        int32  `json:"port"`
	TargetPort  string `json:"target_port"`
	NodePort    int32  `json:"node_port"`
}

type ServiceSpec struct {
	Type                      string            `json:"type"`
	Headless                  bool              `json:"headless"`
	Selector                  map[string]string `json:"selector"`
	Ports                     []ServicePort     `json:"ports"`
	ExternalName              string            `json:"external_name"`
	SessionAffinity           string            `json:"session_affinity"`
	ExternalTrafficPolicy     string            `json:"external_traffic_policy"`
	InternalTrafficPolicy     string            `json:"internal_traffic_policy"`
	PublishNotReadyAddresses  bool              `json:"publish_not_ready_addresses"`
	AllocateLoadBalancerPorts *bool             `json:"allocate_load_balancer_node_ports"`
}

type LoadBalancerAddress struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

type ServiceView struct {
	Spec                ServiceSpec           `json:"spec"`
	ClusterIPs          []string              `json:"cluster_ips"`
	IPFamilies          []string              `json:"ip_families"`
	IPFamilyPolicy      string                `json:"ip_family_policy"`
	LoadBalancerIngress []LoadBalancerAddress `json:"load_balancer_ingress"`
}

type IngressServiceBackend struct {
	Name       string `json:"name"`
	PortName   string `json:"port_name"`
	PortNumber int32  `json:"port_number"`
}

type IngressPath struct {
	Path     string                `json:"path"`
	PathType string                `json:"path_type"`
	Backend  IngressServiceBackend `json:"backend"`
}

type IngressRule struct {
	Host  string        `json:"host"`
	Paths []IngressPath `json:"paths"`
}

type IngressTLS struct {
	Hosts      []string `json:"hosts"`
	SecretName string   `json:"secret_name"`
}

type IngressSpec struct {
	IngressClassName string                 `json:"ingress_class_name"`
	DefaultBackend   *IngressServiceBackend `json:"default_backend"`
	Rules            []IngressRule          `json:"rules"`
	TLS              []IngressTLS           `json:"tls"`
}

type IngressView struct {
	Spec                IngressSpec           `json:"spec"`
	LoadBalancerIngress []LoadBalancerAddress `json:"load_balancer_ingress"`
}

type GatewayAddress struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type GatewayObjectReference struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type GatewayRouteKind struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
}

type GatewayAllowedRoutes struct {
	NamespacesFrom string             `json:"namespaces_from"`
	Selector       *WorkloadSelector  `json:"selector,omitempty"`
	Kinds          []GatewayRouteKind `json:"kinds"`
}

type GatewayTLS struct {
	Mode            string                   `json:"mode"`
	CertificateRefs []GatewayObjectReference `json:"certificate_refs"`
}

type GatewayListener struct {
	Name          string               `json:"name"`
	Hostname      string               `json:"hostname"`
	Port          int32                `json:"port"`
	Protocol      string               `json:"protocol"`
	TLS           *GatewayTLS          `json:"tls,omitempty"`
	AllowedRoutes GatewayAllowedRoutes `json:"allowed_routes"`
}

type GatewaySpec struct {
	GatewayClassName string            `json:"gateway_class_name"`
	Addresses        []GatewayAddress  `json:"addresses"`
	Listeners        []GatewayListener `json:"listeners"`
}

type ResourceCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	ObservedGeneration int64     `json:"observed_generation"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type GatewayListenerStatus struct {
	Name           string              `json:"name"`
	AttachedRoutes int32               `json:"attached_routes"`
	Conditions     []ResourceCondition `json:"conditions"`
}

type GatewayView struct {
	Spec       GatewaySpec             `json:"spec"`
	Addresses  []GatewayAddress        `json:"addresses"`
	Conditions []ResourceCondition     `json:"conditions"`
	Listeners  []GatewayListenerStatus `json:"listeners"`
}

type CreateNetworkingResourceInput struct {
	ClusterID      string
	Namespace      string
	Resource       NetworkingResource
	Name           string
	Labels         map[string]string
	Annotations    map[string]string
	Service        *ServiceSpec
	Ingress        *IngressSpec
	Gateway        *GatewaySpec
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdateNetworkingResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        NetworkingResource
	Name            string
	UID             string
	ResourceVersion string
	Service         *ServiceSpec
	Ingress         *IngressSpec
	Gateway         *GatewaySpec
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

type DeleteNetworkingResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        NetworkingResource
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func ParseNetworkingResource(value string) (NetworkingResource, bool) {
	resourceName := NetworkingResource(value)
	_, exists := networkingResourceIdentities[resourceName]
	return resourceName, exists
}

func NetworkingResourceIdentity(resourceName NetworkingResource) (ResourceIdentity, bool) {
	identity, exists := networkingResourceIdentities[resourceName]
	return identity, exists
}

func (service *Service) ListNetworkingResources(
	ctx context.Context,
	input ListNetworkingResourcesInput,
) (NetworkingResourcePage, error) {
	identity, err := service.networkingIdentity(ctx, input.ClusterID, input.Resource)
	if err != nil || len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return NetworkingResourcePage{}, firstNetworkingError(err)
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return NetworkingResourcePage{}, err
	}
	result := NetworkingResourcePage{
		Resources:     make([]NetworkingResourceSummary, 0, len(page.Items)),
		ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := networkingResourceDetail(item, input.Resource, input.Namespace, "")
		if err != nil {
			return NetworkingResourcePage{}, err
		}
		result.Resources = append(result.Resources, detail.NetworkingResourceSummary)
	}
	return result, nil
}

func (service *Service) GetNetworkingResource(
	ctx context.Context,
	clusterID string,
	namespace string,
	resourceName NetworkingResource,
	name string,
) (NetworkingResourceDetail, error) {
	identity, err := service.networkingIdentity(ctx, clusterID, resourceName)
	if err != nil || len(k8svalidation.IsDNS1123Label(namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return NetworkingResourceDetail{}, firstNetworkingError(err)
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	return networkingResourceDetail(object, resourceName, namespace, name)
}

func (service *Service) CreateNetworkingResource(
	ctx context.Context,
	input CreateNetworkingResourceInput,
) (NetworkingResourceDetail, error) {
	identity, err := service.networkingIdentity(ctx, input.ClusterID, input.Resource)
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	object, err := createNetworkingObject(input)
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace,
		Object: object, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	return networkingResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) UpdateNetworkingResource(
	ctx context.Context,
	input UpdateNetworkingResourceInput,
) (NetworkingResourceDetail, error) {
	identity, err := service.networkingIdentity(ctx, input.ClusterID, input.Resource)
	if err != nil || !validNetworkingMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) ||
		!validNetworkingSpec(input.Resource, input.Service, input.Ingress, input.Gateway) {
		return NetworkingResourceDetail{}, firstNetworkingError(err)
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
	})
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return NetworkingResourceDetail{}, ErrUpstreamConflict
	}
	updated, err := updateNetworkingObject(existing, input)
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		Object: updated, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return NetworkingResourceDetail{}, err
	}
	return networkingResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) DeleteNetworkingResource(
	ctx context.Context,
	input DeleteNetworkingResourceInput,
) error {
	identity, err := service.networkingIdentity(ctx, input.ClusterID, input.Resource)
	if err != nil || !validNetworkingMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return firstNetworkingError(err)
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
	})
}

func (service *Service) networkingIdentity(
	ctx context.Context,
	clusterID string,
	resourceName NetworkingResource,
) (ResourceIdentity, error) {
	identity, exists := networkingResourceIdentities[resourceName]
	if !exists {
		return ResourceIdentity{}, ErrInvalidInput
	}
	if resourceName != NetworkingGateways {
		return identity, nil
	}
	catalog, err := service.DiscoverResources(ctx, clusterID)
	if err != nil {
		return ResourceIdentity{}, err
	}
	for _, discovered := range catalog.Resources {
		if discovered.Group == identity.Group && discovered.Version == identity.Version &&
			discovered.Resource == identity.Resource && discovered.Kind == "Gateway" && discovered.Namespaced {
			return identity, nil
		}
	}
	if catalog.Partial {
		return ResourceIdentity{}, ErrClusterUnavailable
	}
	return ResourceIdentity{}, ErrGatewayAPIUnavailable
}

func firstNetworkingError(err error) error {
	if err != nil {
		return err
	}
	return ErrInvalidInput
}

func validNetworkingMutationIdentity(namespace, name, uid, resourceVersion string) bool {
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0 &&
		len(k8svalidation.IsDNS1123Subdomain(name)) == 0 &&
		strings.TrimSpace(uid) != "" && len(uid) <= 128 &&
		strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func validNetworkingMetadata(namespace, name string, labels, annotations map[string]string) bool {
	if len(k8svalidation.IsDNS1123Label(namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 ||
		!validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxNetworkingAnnotations {
			return false
		}
	}
	return true
}

func validNetworkingSpec(
	resourceName NetworkingResource,
	serviceSpec *ServiceSpec,
	ingressSpec *IngressSpec,
	gatewaySpec *GatewaySpec,
) bool {
	switch resourceName {
	case NetworkingServices:
		return serviceSpec != nil && ingressSpec == nil && gatewaySpec == nil && validServiceSpec(*serviceSpec)
	case NetworkingIngresses:
		return serviceSpec == nil && ingressSpec != nil && gatewaySpec == nil && validIngressSpec(*ingressSpec)
	case NetworkingGateways:
		return serviceSpec == nil && ingressSpec == nil && gatewaySpec != nil && validGatewaySpec(*gatewaySpec)
	default:
		return false
	}
}

func validServiceSpec(spec ServiceSpec) bool {
	serviceType := corev1.ServiceType(spec.Type)
	if serviceType == "" {
		serviceType = corev1.ServiceTypeClusterIP
	}
	switch serviceType {
	case "", corev1.ServiceTypeClusterIP, corev1.ServiceTypeNodePort,
		corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeExternalName:
	default:
		return false
	}
	if len(spec.Ports) > maxNetworkingPorts ||
		!validNamespaceLabels(spec.Selector) ||
		!validTrafficPolicy(spec.ExternalTrafficPolicy) ||
		!validTrafficPolicy(spec.InternalTrafficPolicy) {
		return false
	}
	if spec.SessionAffinity != "" && spec.SessionAffinity != string(corev1.ServiceAffinityNone) &&
		spec.SessionAffinity != string(corev1.ServiceAffinityClientIP) {
		return false
	}
	if spec.Headless && serviceType != corev1.ServiceTypeClusterIP ||
		spec.AllocateLoadBalancerPorts != nil && serviceType != corev1.ServiceTypeLoadBalancer ||
		spec.ExternalTrafficPolicy != "" && serviceType != corev1.ServiceTypeNodePort &&
			serviceType != corev1.ServiceTypeLoadBalancer ||
		spec.InternalTrafficPolicy != "" && serviceType == corev1.ServiceTypeExternalName {
		return false
	}
	if serviceType == corev1.ServiceTypeExternalName {
		if len(k8svalidation.IsDNS1123Subdomain(spec.ExternalName)) != 0 || spec.Headless || len(spec.Selector) != 0 {
			return false
		}
	} else if spec.ExternalName != "" || len(spec.Ports) == 0 {
		return false
	}
	names := map[string]struct{}{}
	portKeys := map[string]struct{}{}
	for _, port := range spec.Ports {
		if (len(spec.Ports) > 1 && port.Name == "") ||
			(port.Name != "" && len(k8svalidation.IsValidPortName(port.Name)) != 0) ||
			port.Port < 1 || port.Port > 65535 || port.NodePort < 0 || port.NodePort > 65535 ||
			!validServiceProtocol(port.Protocol) || !validServiceTargetPort(port.TargetPort) ||
			(port.AppProtocol != "" && len(k8svalidation.IsQualifiedName(port.AppProtocol)) != 0) {
			return false
		}
		if port.NodePort != 0 && serviceType != corev1.ServiceTypeNodePort &&
			serviceType != corev1.ServiceTypeLoadBalancer {
			return false
		}
		if _, exists := names[port.Name]; port.Name != "" && exists {
			return false
		}
		names[port.Name] = struct{}{}
		protocol := port.Protocol
		if protocol == "" {
			protocol = string(corev1.ProtocolTCP)
		}
		key := protocol + "/" + strconv.Itoa(int(port.Port))
		if _, exists := portKeys[key]; exists {
			return false
		}
		portKeys[key] = struct{}{}
	}
	return true
}

func validTrafficPolicy(value string) bool {
	return value == "" || value == string(corev1.ServiceExternalTrafficPolicyCluster) ||
		value == string(corev1.ServiceExternalTrafficPolicyLocal)
}

func validServiceProtocol(value string) bool {
	switch corev1.Protocol(value) {
	case "", corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
		return true
	default:
		return false
	}
}

func validServiceTargetPort(value string) bool {
	if value == "" {
		return true
	}
	if number, err := strconv.ParseInt(value, 10, 32); err == nil {
		return number >= 1 && number <= 65535
	}
	return len(k8svalidation.IsValidPortName(value)) == 0
}

func validIngressSpec(spec IngressSpec) bool {
	if spec.IngressClassName != "" && len(k8svalidation.IsDNS1123Subdomain(spec.IngressClassName)) != 0 {
		return false
	}
	if len(spec.Rules) > maxIngressRules || (spec.DefaultBackend == nil && len(spec.Rules) == 0) ||
		(spec.DefaultBackend != nil && !validIngressBackend(*spec.DefaultBackend)) {
		return false
	}
	for _, rule := range spec.Rules {
		if !validNetworkingHostname(rule.Host) ||
			len(rule.Paths) == 0 || len(rule.Paths) > maxIngressPaths {
			return false
		}
		for _, path := range rule.Paths {
			if path.Path != "" && !strings.HasPrefix(path.Path, "/") ||
				!validIngressPathType(path.PathType) || !validIngressBackend(path.Backend) {
				return false
			}
		}
	}
	for _, tls := range spec.TLS {
		if tls.SecretName != "" && len(k8svalidation.IsDNS1123Subdomain(tls.SecretName)) != 0 {
			return false
		}
		for _, host := range tls.Hosts {
			if !validNetworkingHostname(host) {
				return false
			}
		}
	}
	return true
}

func validIngressBackend(backend IngressServiceBackend) bool {
	return len(k8svalidation.IsDNS1035Label(backend.Name)) == 0 &&
		((backend.PortNumber >= 1 && backend.PortNumber <= 65535 && backend.PortName == "") ||
			(backend.PortNumber == 0 && len(k8svalidation.IsValidPortName(backend.PortName)) == 0))
}

func validIngressPathType(value string) bool {
	switch networkingv1.PathType(value) {
	case networkingv1.PathTypeExact, networkingv1.PathTypePrefix, networkingv1.PathTypeImplementationSpecific:
		return true
	default:
		return false
	}
}

func validGatewaySpec(spec GatewaySpec) bool {
	if len(k8svalidation.IsDNS1123Subdomain(spec.GatewayClassName)) != 0 ||
		len(spec.Addresses) > maxGatewayAddresses || len(spec.Listeners) == 0 ||
		len(spec.Listeners) > maxGatewayListeners {
		return false
	}
	for _, address := range spec.Addresses {
		if address.Value == "" || len(address.Value) > 253 ||
			(address.Type != "" && len(k8svalidation.IsQualifiedName(address.Type)) != 0) {
			return false
		}
		if address.Type == "IPAddress" && net.ParseIP(address.Value) == nil {
			return false
		}
	}
	names := map[string]struct{}{}
	for _, listener := range spec.Listeners {
		if len(k8svalidation.IsDNS1123Label(listener.Name)) != 0 || listener.Port < 1 || listener.Port > 65535 ||
			!validGatewayProtocol(listener.Protocol) ||
			!validNetworkingHostname(listener.Hostname) ||
			!validGatewayTLS(listener.Protocol, listener.TLS) || !validGatewayAllowedRoutes(listener.AllowedRoutes) {
			return false
		}
		if _, exists := names[listener.Name]; exists {
			return false
		}
		names[listener.Name] = struct{}{}
	}
	return true
}

func validNetworkingHostname(value string) bool {
	return value == "" || len(k8svalidation.IsDNS1123Subdomain(value)) == 0 ||
		len(k8svalidation.IsWildcardDNS1123Subdomain(value)) == 0
}

func validGatewayProtocol(value string) bool {
	switch value {
	case "HTTP", "HTTPS", "TLS", "TCP", "UDP":
		return true
	default:
		return false
	}
}

func validGatewayTLS(protocol string, tls *GatewayTLS) bool {
	if tls == nil {
		return protocol != "HTTPS" && protocol != "TLS"
	}
	if protocol != "HTTPS" && protocol != "TLS" || (tls.Mode != "Terminate" && tls.Mode != "Passthrough") {
		return false
	}
	if protocol == "HTTPS" && tls.Mode != "Terminate" {
		return false
	}
	if len(tls.CertificateRefs) > maxGatewayCertificateRefs ||
		tls.Mode == "Terminate" && len(tls.CertificateRefs) == 0 ||
		tls.Mode == "Passthrough" && len(tls.CertificateRefs) != 0 {
		return false
	}
	for _, reference := range tls.CertificateRefs {
		if len(k8svalidation.IsDNS1123Subdomain(reference.Name)) != 0 ||
			(reference.Namespace != "" && len(k8svalidation.IsDNS1123Label(reference.Namespace)) != 0) ||
			(reference.Group != "" && len(k8svalidation.IsDNS1123Subdomain(reference.Group)) != 0) ||
			(reference.Kind != "" && len(k8svalidation.IsQualifiedName(reference.Kind)) != 0) {
			return false
		}
	}
	return true
}

func validGatewayAllowedRoutes(routes GatewayAllowedRoutes) bool {
	if len(routes.Kinds) > maxGatewayRouteKinds {
		return false
	}
	switch routes.NamespacesFrom {
	case "", "Same", "All":
		if routes.Selector != nil {
			return false
		}
	case "Selector":
		if routes.Selector == nil {
			return false
		}
		if _, err := metav1.LabelSelectorAsSelector(gatewayLabelSelector(routes.Selector)); err != nil {
			return false
		}
	default:
		return false
	}
	for _, kind := range routes.Kinds {
		if len(k8svalidation.IsQualifiedName(kind.Kind)) != 0 ||
			(kind.Group != "" && len(k8svalidation.IsDNS1123Subdomain(kind.Group)) != 0) {
			return false
		}
	}
	return true
}

func createNetworkingObject(input CreateNetworkingResourceInput) (map[string]any, error) {
	if !validNetworkingMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) ||
		!validNetworkingSpec(input.Resource, input.Service, input.Ingress, input.Gateway) ||
		input.Resource == NetworkingServices && len(k8svalidation.IsDNS1035Label(input.Name)) != 0 {
		return nil, ErrInvalidInput
	}
	metadata := metav1.ObjectMeta{
		Name: input.Name, Namespace: input.Namespace,
		Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
	}
	var object any
	switch input.Resource {
	case NetworkingServices:
		object = &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metadata, Spec: serviceKubernetesSpec(*input.Service, nil)}
	case NetworkingIngresses:
		object = &networkingv1.Ingress{TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"}, ObjectMeta: metadata, Spec: ingressKubernetesSpec(*input.Ingress)}
	case NetworkingGateways:
		object = gatewayObject{APIVersion: "gateway.networking.k8s.io/v1", Kind: "Gateway", Metadata: metadata, Spec: gatewayWireSpec(*input.Gateway)}
	default:
		return nil, ErrInvalidInput
	}
	body, err := json.Marshal(object)
	if err != nil {
		return nil, ErrInvalidInput
	}
	var result unstructured.Unstructured
	if result.UnmarshalJSON(body) != nil {
		return nil, ErrInvalidInput
	}
	return result.Object, nil
}

func updateNetworkingObject(existing map[string]any, input UpdateNetworkingResourceInput) (map[string]any, error) {
	switch input.Resource {
	case NetworkingServices:
		var object corev1.Service
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		requestedType := corev1.ServiceType(input.Service.Type)
		if requestedType == "" {
			requestedType = corev1.ServiceTypeClusterIP
		}
		currentHeadless := object.Spec.ClusterIP == corev1.ClusterIPNone
		if object.Spec.Type != corev1.ServiceTypeExternalName && requestedType != corev1.ServiceTypeExternalName &&
			currentHeadless != input.Service.Headless {
			return nil, ErrInvalidInput
		}
		object.Spec = serviceKubernetesSpec(*input.Service, &object.Spec)
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case NetworkingIngresses:
		var object networkingv1.Ingress
		if runtime.DefaultUnstructuredConverter.FromUnstructured(existing, &object) != nil {
			return nil, ErrInvalidResponse
		}
		object.Spec = ingressKubernetesSpec(*input.Ingress)
		return runtime.DefaultUnstructuredConverter.ToUnstructured(&object)
	case NetworkingGateways:
		copy := runtime.DeepCopyJSONValue(existing).(map[string]any)
		wireSpec := gatewayWireSpec(*input.Gateway)
		spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&wireSpec)
		if err != nil {
			return nil, ErrInvalidInput
		}
		currentSpec, found, err := unstructured.NestedMap(copy, "spec")
		if err != nil || !found {
			return nil, ErrInvalidResponse
		}
		for _, field := range []string{"gatewayClassName", "listeners"} {
			currentSpec[field] = runtime.DeepCopyJSONValue(spec[field])
		}
		if addresses, exists := spec["addresses"]; exists {
			currentSpec["addresses"] = runtime.DeepCopyJSONValue(addresses)
		} else {
			delete(currentSpec, "addresses")
		}
		copy["spec"] = currentSpec
		return copy, nil
	default:
		return nil, ErrInvalidInput
	}
}

func serviceKubernetesSpec(input ServiceSpec, current *corev1.ServiceSpec) corev1.ServiceSpec {
	result := corev1.ServiceSpec{
		Type: corev1.ServiceType(input.Type), Selector: maps.Clone(input.Selector),
		Ports: serviceKubernetesPorts(input.Ports), ExternalName: input.ExternalName,
		SessionAffinity:               corev1.ServiceAffinity(input.SessionAffinity),
		ExternalTrafficPolicy:         corev1.ServiceExternalTrafficPolicy(input.ExternalTrafficPolicy),
		PublishNotReadyAddresses:      input.PublishNotReadyAddresses,
		AllocateLoadBalancerNodePorts: copyPointer(input.AllocateLoadBalancerPorts),
	}
	if result.Type == "" {
		result.Type = corev1.ServiceTypeClusterIP
	}
	if result.SessionAffinity == "" {
		result.SessionAffinity = corev1.ServiceAffinityNone
	}
	if input.InternalTrafficPolicy != "" {
		policy := corev1.ServiceInternalTrafficPolicy(input.InternalTrafficPolicy)
		result.InternalTrafficPolicy = &policy
	}
	if input.Headless {
		result.ClusterIP = corev1.ClusterIPNone
		result.ClusterIPs = []string{corev1.ClusterIPNone}
	}
	if current != nil && current.Type != corev1.ServiceTypeExternalName && result.Type != corev1.ServiceTypeExternalName {
		result.ClusterIP = current.ClusterIP
		result.ClusterIPs = append([]string(nil), current.ClusterIPs...)
		result.IPFamilies = append([]corev1.IPFamily(nil), current.IPFamilies...)
		result.IPFamilyPolicy = copyPointer(current.IPFamilyPolicy)
		result.ExternalIPs = append([]string(nil), current.ExternalIPs...)
		result.TrafficDistribution = copyPointer(current.TrafficDistribution)
		if result.SessionAffinity == corev1.ServiceAffinityClientIP {
			result.SessionAffinityConfig = current.SessionAffinityConfig.DeepCopy()
		}
		if result.Type == corev1.ServiceTypeNodePort || result.Type == corev1.ServiceTypeLoadBalancer {
			preserveServiceNodePorts(result.Ports, current.Ports)
		}
		if result.Type == corev1.ServiceTypeLoadBalancer {
			result.LoadBalancerIP = current.LoadBalancerIP
			result.LoadBalancerSourceRanges = append([]string(nil), current.LoadBalancerSourceRanges...)
			result.LoadBalancerClass = copyPointer(current.LoadBalancerClass)
			if input.AllocateLoadBalancerPorts == nil {
				result.AllocateLoadBalancerNodePorts = copyPointer(current.AllocateLoadBalancerNodePorts)
			}
		}
		if result.Type == corev1.ServiceTypeLoadBalancer &&
			result.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal {
			result.HealthCheckNodePort = current.HealthCheckNodePort
		}
	}
	return result
}

func preserveServiceNodePorts(result, current []corev1.ServicePort) {
	for index := range result {
		if result[index].NodePort != 0 {
			continue
		}
		for _, existing := range current {
			matched := result[index].Name != "" && result[index].Name == existing.Name
			if !matched {
				matched = result[index].Port == existing.Port && result[index].Protocol == existing.Protocol
			}
			if matched {
				result[index].NodePort = existing.NodePort
				break
			}
		}
	}
}

func serviceKubernetesPorts(input []ServicePort) []corev1.ServicePort {
	result := make([]corev1.ServicePort, 0, len(input))
	for _, port := range input {
		protocol := corev1.Protocol(port.Protocol)
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		result = append(result, corev1.ServicePort{
			Name: port.Name, Protocol: protocol, AppProtocol: stringPointer(port.AppProtocol),
			Port: port.Port, TargetPort: parseTargetPort(port.TargetPort), NodePort: port.NodePort,
		})
	}
	return result
}

func parseTargetPort(value string) intstr.IntOrString {
	if value == "" {
		return intstr.IntOrString{}
	}
	if number, err := strconv.ParseInt(value, 10, 32); err == nil {
		return intstr.FromInt32(int32(number))
	}
	return intstr.FromString(value)
}

func ingressKubernetesSpec(input IngressSpec) networkingv1.IngressSpec {
	result := networkingv1.IngressSpec{IngressClassName: stringPointer(input.IngressClassName)}
	if input.DefaultBackend != nil {
		backend := ingressKubernetesBackend(*input.DefaultBackend)
		result.DefaultBackend = &backend
	}
	for _, rule := range input.Rules {
		paths := make([]networkingv1.HTTPIngressPath, 0, len(rule.Paths))
		for _, path := range rule.Paths {
			pathType := networkingv1.PathType(path.PathType)
			paths = append(paths, networkingv1.HTTPIngressPath{
				Path: path.Path, PathType: &pathType, Backend: ingressKubernetesBackend(path.Backend),
			})
		}
		result.Rules = append(result.Rules, networkingv1.IngressRule{
			Host: rule.Host, IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
			},
		})
	}
	for _, tls := range input.TLS {
		result.TLS = append(result.TLS, networkingv1.IngressTLS{Hosts: append([]string(nil), tls.Hosts...), SecretName: tls.SecretName})
	}
	return result
}

func ingressKubernetesBackend(input IngressServiceBackend) networkingv1.IngressBackend {
	port := networkingv1.ServiceBackendPort{Name: input.PortName, Number: input.PortNumber}
	return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: input.Name, Port: port}}
}

func networkingResourceDetail(
	object map[string]any,
	resourceName NetworkingResource,
	namespace string,
	name string,
) (NetworkingResourceDetail, error) {
	metadata := &unstructured.Unstructured{Object: object}
	if metadata.GetNamespace() != namespace || metadata.GetName() == "" ||
		(name != "" && metadata.GetName() != name) {
		return NetworkingResourceDetail{}, ErrInvalidResponse
	}
	summary := NetworkingResourceSummary{
		Resource: resourceName, APIVersion: metadata.GetAPIVersion(), Kind: metadata.GetKind(),
		Namespace: namespace, Name: metadata.GetName(), UID: string(metadata.GetUID()),
		ResourceVersion: metadata.GetResourceVersion(), CreationTimestamp: metadata.GetCreationTimestamp().Time,
		Labels: cloneStringMap(metadata.GetLabels()),
	}
	switch resourceName {
	case NetworkingServices:
		var value corev1.Service
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &value) != nil ||
			value.APIVersion != "v1" || value.Kind != "Service" {
			return NetworkingResourceDetail{}, ErrInvalidResponse
		}
		view := serviceView(value)
		summary.Service = &view
	case NetworkingIngresses:
		var value networkingv1.Ingress
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &value) != nil ||
			value.APIVersion != "networking.k8s.io/v1" || value.Kind != "Ingress" {
			return NetworkingResourceDetail{}, ErrInvalidResponse
		}
		view := ingressView(value)
		summary.Ingress = &view
	case NetworkingGateways:
		var value gatewayObject
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &value) != nil ||
			value.APIVersion != "gateway.networking.k8s.io/v1" || value.Kind != "Gateway" {
			return NetworkingResourceDetail{}, ErrInvalidResponse
		}
		view := gatewayView(value)
		summary.Gateway = &view
	default:
		return NetworkingResourceDetail{}, ErrInvalidInput
	}
	return NetworkingResourceDetail{NetworkingResourceSummary: summary, Annotations: cloneStringMap(metadata.GetAnnotations())}, nil
}

func serviceView(value corev1.Service) ServiceView {
	ports := make([]ServicePort, 0, len(value.Spec.Ports))
	for _, port := range value.Spec.Ports {
		appProtocol := ""
		if port.AppProtocol != nil {
			appProtocol = *port.AppProtocol
		}
		ports = append(ports, ServicePort{Name: port.Name, Protocol: string(port.Protocol), AppProtocol: appProtocol,
			Port: port.Port, TargetPort: port.TargetPort.String(), NodePort: port.NodePort})
	}
	view := ServiceView{Spec: ServiceSpec{
		Type: string(value.Spec.Type), Headless: value.Spec.ClusterIP == corev1.ClusterIPNone,
		Selector: cloneStringMap(value.Spec.Selector), Ports: ports, ExternalName: value.Spec.ExternalName,
		SessionAffinity: string(value.Spec.SessionAffinity), ExternalTrafficPolicy: string(value.Spec.ExternalTrafficPolicy),
		PublishNotReadyAddresses:  value.Spec.PublishNotReadyAddresses,
		AllocateLoadBalancerPorts: copyPointer(value.Spec.AllocateLoadBalancerNodePorts),
	}, ClusterIPs: append([]string{}, value.Spec.ClusterIPs...), IPFamilies: make([]string, 0, len(value.Spec.IPFamilies)),
		IPFamilyPolicy:      pointerString(value.Spec.IPFamilyPolicy),
		LoadBalancerIngress: make([]LoadBalancerAddress, 0, len(value.Status.LoadBalancer.Ingress))}
	if value.Spec.InternalTrafficPolicy != nil {
		view.Spec.InternalTrafficPolicy = string(*value.Spec.InternalTrafficPolicy)
	}
	for _, family := range value.Spec.IPFamilies {
		view.IPFamilies = append(view.IPFamilies, string(family))
	}
	for _, address := range value.Status.LoadBalancer.Ingress {
		view.LoadBalancerIngress = append(view.LoadBalancerIngress, LoadBalancerAddress{IP: address.IP, Hostname: address.Hostname})
	}
	return view
}

func ingressView(value networkingv1.Ingress) IngressView {
	view := IngressView{
		Spec: IngressSpec{IngressClassName: pointerString(value.Spec.IngressClassName),
			Rules: make([]IngressRule, 0, len(value.Spec.Rules)), TLS: make([]IngressTLS, 0, len(value.Spec.TLS))},
		LoadBalancerIngress: make([]LoadBalancerAddress, 0, len(value.Status.LoadBalancer.Ingress)),
	}
	if value.Spec.DefaultBackend != nil {
		backend := ingressBackendView(*value.Spec.DefaultBackend)
		view.Spec.DefaultBackend = &backend
	}
	for _, rule := range value.Spec.Rules {
		result := IngressRule{Host: rule.Host, Paths: make([]IngressPath, 0)}
		if rule.HTTP != nil {
			for _, path := range rule.HTTP.Paths {
				result.Paths = append(result.Paths, IngressPath{Path: path.Path, PathType: pointerString(path.PathType), Backend: ingressBackendView(path.Backend)})
			}
		}
		view.Spec.Rules = append(view.Spec.Rules, result)
	}
	for _, tls := range value.Spec.TLS {
		view.Spec.TLS = append(view.Spec.TLS, IngressTLS{Hosts: append([]string{}, tls.Hosts...), SecretName: tls.SecretName})
	}
	for _, address := range value.Status.LoadBalancer.Ingress {
		view.LoadBalancerIngress = append(view.LoadBalancerIngress, LoadBalancerAddress{IP: address.IP, Hostname: address.Hostname})
	}
	return view
}

func ingressBackendView(value networkingv1.IngressBackend) IngressServiceBackend {
	if value.Service == nil {
		return IngressServiceBackend{}
	}
	return IngressServiceBackend{Name: value.Service.Name, PortName: value.Service.Port.Name, PortNumber: value.Service.Port.Number}
}

func pointerString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	return maps.Clone(value)
}

// Gateway API is a CRD, so these private wire types keep the public ZKE model
// independent of a particular controller while preserving the v1 core fields.
type gatewayObject struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metav1.ObjectMeta `json:"metadata"`
	Spec       gatewaySpecWire   `json:"spec"`
	Status     gatewayStatusWire `json:"status,omitempty"`
}

type gatewaySpecWire struct {
	GatewayClassName string                `json:"gatewayClassName"`
	Addresses        []gatewayAddressWire  `json:"addresses,omitempty"`
	Listeners        []gatewayListenerWire `json:"listeners"`
}

type gatewayAddressWire struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
}
type gatewayReferenceWire struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}
type gatewayRouteKindWire struct {
	Group string `json:"group,omitempty"`
	Kind  string `json:"kind"`
}
type gatewayAllowedRoutesWire struct {
	Namespaces *gatewayNamespacesWire `json:"namespaces,omitempty"`
	Kinds      []gatewayRouteKindWire `json:"kinds,omitempty"`
}
type gatewayNamespacesWire struct {
	From     string                `json:"from,omitempty"`
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}
type gatewayTLSWire struct {
	Mode            string                 `json:"mode,omitempty"`
	CertificateRefs []gatewayReferenceWire `json:"certificateRefs,omitempty"`
}
type gatewayListenerWire struct {
	Name          string                    `json:"name"`
	Hostname      string                    `json:"hostname,omitempty"`
	Port          int32                     `json:"port"`
	Protocol      string                    `json:"protocol"`
	TLS           *gatewayTLSWire           `json:"tls,omitempty"`
	AllowedRoutes *gatewayAllowedRoutesWire `json:"allowedRoutes,omitempty"`
}
type gatewayStatusWire struct {
	Addresses  []gatewayAddressWire        `json:"addresses,omitempty"`
	Conditions []metav1.Condition          `json:"conditions,omitempty"`
	Listeners  []gatewayListenerStatusWire `json:"listeners,omitempty"`
}
type gatewayListenerStatusWire struct {
	Name           string             `json:"name"`
	AttachedRoutes int32              `json:"attachedRoutes"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
}

func gatewayWireSpec(input GatewaySpec) gatewaySpecWire {
	result := gatewaySpecWire{GatewayClassName: input.GatewayClassName}
	for _, address := range input.Addresses {
		result.Addresses = append(result.Addresses, gatewayAddressWire(address))
	}
	for _, listener := range input.Listeners {
		wire := gatewayListenerWire{Name: listener.Name, Hostname: listener.Hostname, Port: listener.Port, Protocol: listener.Protocol}
		if listener.TLS != nil {
			wire.TLS = &gatewayTLSWire{Mode: listener.TLS.Mode}
			for _, reference := range listener.TLS.CertificateRefs {
				wire.TLS.CertificateRefs = append(wire.TLS.CertificateRefs, gatewayReferenceWire(reference))
			}
		}
		if listener.AllowedRoutes.NamespacesFrom != "" || listener.AllowedRoutes.Selector != nil || len(listener.AllowedRoutes.Kinds) > 0 {
			wire.AllowedRoutes = &gatewayAllowedRoutesWire{Namespaces: &gatewayNamespacesWire{From: listener.AllowedRoutes.NamespacesFrom, Selector: gatewayLabelSelector(listener.AllowedRoutes.Selector)}}
			for _, kind := range listener.AllowedRoutes.Kinds {
				wire.AllowedRoutes.Kinds = append(wire.AllowedRoutes.Kinds, gatewayRouteKindWire(kind))
			}
		}
		result.Listeners = append(result.Listeners, wire)
	}
	return result
}

func gatewayView(value gatewayObject) GatewayView {
	view := GatewayView{
		Spec: GatewaySpec{GatewayClassName: value.Spec.GatewayClassName,
			Addresses: make([]GatewayAddress, 0, len(value.Spec.Addresses)),
			Listeners: make([]GatewayListener, 0, len(value.Spec.Listeners))},
		Addresses: make([]GatewayAddress, 0, len(value.Status.Addresses)),
		Listeners: make([]GatewayListenerStatus, 0, len(value.Status.Listeners)),
	}
	for _, address := range value.Spec.Addresses {
		view.Spec.Addresses = append(view.Spec.Addresses, GatewayAddress(address))
	}
	for _, listener := range value.Spec.Listeners {
		item := GatewayListener{Name: listener.Name, Hostname: listener.Hostname, Port: listener.Port, Protocol: listener.Protocol,
			AllowedRoutes: GatewayAllowedRoutes{Kinds: make([]GatewayRouteKind, 0)}}
		if listener.TLS != nil {
			item.TLS = &GatewayTLS{Mode: listener.TLS.Mode,
				CertificateRefs: make([]GatewayObjectReference, 0, len(listener.TLS.CertificateRefs))}
			for _, reference := range listener.TLS.CertificateRefs {
				item.TLS.CertificateRefs = append(item.TLS.CertificateRefs, GatewayObjectReference(reference))
			}
		}
		if listener.AllowedRoutes != nil {
			if listener.AllowedRoutes.Namespaces != nil {
				item.AllowedRoutes.NamespacesFrom = listener.AllowedRoutes.Namespaces.From
				item.AllowedRoutes.Selector = gatewaySelectorView(listener.AllowedRoutes.Namespaces.Selector)
			}
			for _, kind := range listener.AllowedRoutes.Kinds {
				item.AllowedRoutes.Kinds = append(item.AllowedRoutes.Kinds, GatewayRouteKind(kind))
			}
		}
		view.Spec.Listeners = append(view.Spec.Listeners, item)
	}
	for _, address := range value.Status.Addresses {
		view.Addresses = append(view.Addresses, GatewayAddress(address))
	}
	view.Conditions = gatewayConditions(value.Status.Conditions)
	for _, listener := range value.Status.Listeners {
		view.Listeners = append(view.Listeners, GatewayListenerStatus{Name: listener.Name, AttachedRoutes: listener.AttachedRoutes, Conditions: gatewayConditions(listener.Conditions)})
	}
	return view
}

func gatewayLabelSelector(input *WorkloadSelector) *metav1.LabelSelector {
	if input == nil {
		return nil
	}
	result := &metav1.LabelSelector{MatchLabels: maps.Clone(input.MatchLabels)}
	for _, expression := range input.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: expression.Key, Operator: metav1.LabelSelectorOperator(expression.Operator),
			Values: append([]string(nil), expression.Values...),
		})
	}
	return result
}

func gatewaySelectorView(input *metav1.LabelSelector) *WorkloadSelector {
	if input == nil {
		return nil
	}
	result := &WorkloadSelector{MatchLabels: cloneStringMap(input.MatchLabels),
		MatchExpressions: make([]WorkloadSelectorRequirement, 0, len(input.MatchExpressions))}
	for _, expression := range input.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadSelectorRequirement{
			Key: expression.Key, Operator: string(expression.Operator),
			Values: append([]string{}, expression.Values...),
		})
	}
	return result
}

func gatewayConditions(input []metav1.Condition) []ResourceCondition {
	result := make([]ResourceCondition, 0, len(input))
	for _, condition := range input {
		result = append(result, ResourceCondition{Type: condition.Type, Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message, ObservedGeneration: condition.ObservedGeneration, LastTransitionTime: condition.LastTransitionTime.Time})
	}
	return result
}
