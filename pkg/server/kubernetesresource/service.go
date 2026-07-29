package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/validation"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	DefaultNodeListLimit      int64 = 100
	MaxNodeListLimit          int64 = 500
	maxSelectorBytes                = 4096
	maxContinueTokenBytes           = 4096
	kubernetesJSONContentType       = "application/json"
)

var (
	ErrInvalidInput           = errors.New("invalid Kubernetes resource input")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support resource queries")
	ErrRequestCapacity        = errors.New("resource query capacity is exhausted")
	ErrNodeNotFound           = errors.New("Kubernetes Node not found")
	ErrResourceNotFound       = errors.New("Kubernetes resource not found")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes API request timed out")
	ErrResponseTooLarge       = errors.New("Kubernetes API response is too large")
	ErrUpstreamConflict       = errors.New("Kubernetes API resource conflict")
	ErrUpstreamFailure        = errors.New("Kubernetes API request failed")
	ErrInvalidResponse        = errors.New("invalid Agent resource response")
)

var nodeResource = &agentv1.GroupVersionResource{
	Version:  "v1",
	Resource: "nodes",
}

type ResourceRequester interface {
	RequestResource(
		ctx context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		requestBody io.Reader,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error)
}

type Service struct {
	requester ResourceRequester
}

type ListNodesInput struct {
	ClusterID     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type NodePage struct {
	Nodes              []NodeSummary
	ContinueToken      string
	ResourceVersion    string
	RemainingItemCount *int64
}

type NodeSummary struct {
	Name              string
	UID               string
	CreationTimestamp time.Time
	Status            string
	Unschedulable     bool
	Roles             []string
	InternalIP        string
	KubernetesVersion string
	OperatingSystem   string
	OSImage           string
	KernelVersion     string
	ContainerRuntime  string
	CPUCapacity       string
	MemoryCapacity    string
	PodsCapacity      string
	CPUAllocatable    string
	MemoryAllocatable string
	PodsAllocatable   string
}

type NodeAddress struct {
	Type    string
	Address string
}

type NodeTaint struct {
	Key       string
	Value     string
	Effect    string
	TimeAdded *time.Time
}

type NodeCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastHeartbeatTime  time.Time
	LastTransitionTime time.Time
}

type NodeDetail struct {
	NodeSummary
	Labels       map[string]string
	Annotations  map[string]string
	ProviderID   string
	PodCIDR      string
	PodCIDRs     []string
	Addresses    []NodeAddress
	Taints       []NodeTaint
	Conditions   []NodeCondition
	Architecture string
	BootID       string
	MachineID    string
	SystemUUID   string
}

func NewService(requester ResourceRequester) *Service {
	return &Service{requester: requester}
}

func (service *Service) ListNodes(
	ctx context.Context,
	input ListNodesInput,
) (NodePage, error) {
	if err := validateListNodesInput(input); err != nil {
		return NodePage{}, err
	}
	var body bytes.Buffer
	response, err := service.requester.RequestResource(
		ctx,
		input.ClusterID,
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource:       nodeResource,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			ListOptions: &agentv1.ListOptions{
				LabelSelector: input.LabelSelector,
				FieldSelector: input.FieldSelector,
				Limit:         uint64(input.Limit),
				ContinueToken: input.ContinueToken,
			},
		},
		nil,
		&body,
	)
	if err != nil {
		return NodePage{}, requestError(err)
	}
	if err := responseError(response); err != nil {
		return NodePage{}, err
	}

	var list corev1.NodeList
	if err := json.Unmarshal(body.Bytes(), &list); err != nil {
		return NodePage{}, fmt.Errorf("%w: decode Node list", ErrInvalidResponse)
	}
	nodes := make([]NodeSummary, 0, len(list.Items))
	for index := range list.Items {
		if list.Items[index].Name == "" {
			return NodePage{}, fmt.Errorf(
				"%w: Node list item has no name",
				ErrInvalidResponse,
			)
		}
		nodes = append(nodes, summarizeNode(&list.Items[index]))
	}
	return NodePage{
		Nodes:              nodes,
		ContinueToken:      list.Continue,
		ResourceVersion:    list.ResourceVersion,
		RemainingItemCount: list.RemainingItemCount,
	}, nil
}

func (service *Service) GetNode(
	ctx context.Context,
	clusterID string,
	name string,
) (NodeDetail, error) {
	if !validation.IsUUID(clusterID) ||
		len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return NodeDetail{}, ErrInvalidInput
	}
	var body bytes.Buffer
	response, err := service.requester.RequestResource(
		ctx,
		clusterID,
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource:       nodeResource,
			Name:           name,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
		&body,
	)
	if err != nil {
		return NodeDetail{}, requestError(err)
	}
	if err := responseError(response); err != nil {
		return NodeDetail{}, err
	}

	var node corev1.Node
	if err := json.Unmarshal(body.Bytes(), &node); err != nil ||
		node.Name == "" ||
		node.Name != name {
		return NodeDetail{}, fmt.Errorf(
			"%w: decode Node detail",
			ErrInvalidResponse,
		)
	}
	return detailNode(&node), nil
}

func validateListNodesInput(input ListNodesInput) error {
	if !validation.IsUUID(input.ClusterID) ||
		input.Limit < 1 ||
		input.Limit > MaxNodeListLimit ||
		len(input.ContinueToken) > maxContinueTokenBytes ||
		strings.TrimSpace(input.ContinueToken) != input.ContinueToken ||
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

func requestError(err error) error {
	switch {
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrResourceCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrResourceRequestExhausted):
		return ErrRequestCapacity
	case errors.Is(err, context.DeadlineExceeded):
		return ErrClusterTimeout
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.ResourceResponse) error {
	return responseErrorWithNotFound(response, ErrNodeNotFound)
}

func responseErrorWithNotFound(
	response *agentv1.ResourceResponse,
	notFound error,
) error {
	if response == nil {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if response.GetKubernetesStatusCode() < 200 ||
			response.GetKubernetesStatusCode() >= 300 ||
			response.GetContentType() != kubernetesJSONContentType ||
			response.GetBodySize() == 0 {
			return ErrInvalidResponse
		}
		return nil
	}
	switch response.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return notFound
	case agentv1.ResultCode_RESULT_CODE_CONFLICT:
		return ErrUpstreamConflict
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		if response.GetKubernetesStatusCode() == 413 {
			return ErrResponseTooLarge
		}
		return ErrRequestCapacity
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return ErrClusterUnavailable
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	case agentv1.ResultCode_RESULT_CODE_INTERNAL:
		return ErrUpstreamFailure
	default:
		return ErrInvalidResponse
	}
}

func summarizeNode(node *corev1.Node) NodeSummary {
	return NodeSummary{
		Name:              node.Name,
		UID:               string(node.UID),
		CreationTimestamp: node.CreationTimestamp.Time,
		Status:            nodeReadyStatus(node),
		Unschedulable:     node.Spec.Unschedulable,
		Roles:             nodeRoles(node.Labels),
		InternalIP:        nodeAddress(node.Status.Addresses, corev1.NodeInternalIP),
		KubernetesVersion: node.Status.NodeInfo.KubeletVersion,
		OperatingSystem:   node.Status.NodeInfo.OperatingSystem,
		OSImage:           node.Status.NodeInfo.OSImage,
		KernelVersion:     node.Status.NodeInfo.KernelVersion,
		ContainerRuntime:  node.Status.NodeInfo.ContainerRuntimeVersion,
		CPUCapacity:       quantity(node.Status.Capacity, corev1.ResourceCPU),
		MemoryCapacity:    quantity(node.Status.Capacity, corev1.ResourceMemory),
		PodsCapacity:      quantity(node.Status.Capacity, corev1.ResourcePods),
		CPUAllocatable:    quantity(node.Status.Allocatable, corev1.ResourceCPU),
		MemoryAllocatable: quantity(node.Status.Allocatable, corev1.ResourceMemory),
		PodsAllocatable:   quantity(node.Status.Allocatable, corev1.ResourcePods),
	}
}

func detailNode(node *corev1.Node) NodeDetail {
	addresses := make([]NodeAddress, 0, len(node.Status.Addresses))
	for _, address := range node.Status.Addresses {
		addresses = append(addresses, NodeAddress{
			Type:    string(address.Type),
			Address: address.Address,
		})
	}
	taints := make([]NodeTaint, 0, len(node.Spec.Taints))
	for _, taint := range node.Spec.Taints {
		var added *time.Time
		if taint.TimeAdded != nil {
			value := taint.TimeAdded.Time
			added = &value
		}
		taints = append(taints, NodeTaint{
			Key:       taint.Key,
			Value:     taint.Value,
			Effect:    string(taint.Effect),
			TimeAdded: added,
		})
	}
	conditions := make([]NodeCondition, 0, len(node.Status.Conditions))
	for _, condition := range node.Status.Conditions {
		conditions = append(conditions, NodeCondition{
			Type:               string(condition.Type),
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastHeartbeatTime:  condition.LastHeartbeatTime.Time,
			LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return NodeDetail{
		NodeSummary:  summarizeNode(node),
		Labels:       cloneMap(node.Labels),
		Annotations:  cloneMap(node.Annotations),
		ProviderID:   node.Spec.ProviderID,
		PodCIDR:      node.Spec.PodCIDR,
		PodCIDRs:     append([]string(nil), node.Spec.PodCIDRs...),
		Addresses:    addresses,
		Taints:       taints,
		Conditions:   conditions,
		Architecture: node.Status.NodeInfo.Architecture,
		BootID:       node.Status.NodeInfo.BootID,
		MachineID:    node.Status.NodeInfo.MachineID,
		SystemUUID:   node.Status.NodeInfo.SystemUUID,
	}
}

func nodeReadyStatus(node *corev1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		switch condition.Status {
		case corev1.ConditionTrue:
			return "ready"
		case corev1.ConditionFalse:
			return "not_ready"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

func nodeRoles(nodeLabels map[string]string) []string {
	const rolePrefix = "node-role.kubernetes.io/"
	roles := make(map[string]struct{})
	for key, value := range nodeLabels {
		if strings.HasPrefix(key, rolePrefix) {
			role := strings.TrimPrefix(key, rolePrefix)
			if role != "" {
				roles[role] = struct{}{}
			}
			continue
		}
		if key == "kubernetes.io/role" && value != "" {
			roles[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(roles))
	for role := range roles {
		result = append(result, role)
	}
	sort.Strings(result)
	return result
}

func nodeAddress(addresses []corev1.NodeAddress, kind corev1.NodeAddressType) string {
	for _, address := range addresses {
		if address.Type == kind {
			return address.Address
		}
	}
	return ""
}

func quantity(resources corev1.ResourceList, name corev1.ResourceName) string {
	value, exists := resources[name]
	if !exists {
		return ""
	}
	return value.String()
}

func cloneMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
