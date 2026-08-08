package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"golang.org/x/sync/semaphore"
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
	// The reasons an Agent uses for a refusal of its own, mirrored from
	// pkg/agent. They are part of the Server–Agent contract: an Agent that
	// predates them reports a plain FORBIDDEN, which still maps to a denial.
	agentResourceNotAllowedReason         = "ResourceNotAllowed"
	agentNamespaceForbiddenReason         = "AgentNamespaceForbidden"
	defaultMaxBufferedResponseBytes int64 = 256 * 1024 * 1024
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
	// The Agent's own refusals, which are not the Kubernetes API Server's. They
	// are permanent boundaries of ZKE rather than gaps in what the Agent's
	// ServiceAccount was granted, so they are neither retried nor answered by
	// widening a ClusterRole.
	ErrResourceNotEnabled      = errors.New("Kubernetes resource is not enabled for the Agent")
	ErrAgentNamespaceForbidden = errors.New("Agent Namespace Secrets are not accessible")
	ErrClusterUnavailable      = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout          = errors.New("Kubernetes API request timed out")
	ErrResponseTooLarge        = errors.New("Kubernetes API response is too large")
	ErrResponseBudget          = errors.New("Server response buffer budget is exhausted")
	ErrIdempotencyConflict     = errors.New("Kubernetes resource idempotency conflict")
	ErrUpstreamConflict        = errors.New("Kubernetes API resource conflict")
	ErrUpstreamRejected        = errors.New("Kubernetes rejected the submitted resource")
	ErrConfigMapImmutable      = errors.New("Kubernetes ConfigMap is immutable")
	ErrManagedResource         = errors.New("Kubernetes resource is managed by ZKE")
	ErrUpstreamFailure         = errors.New("Kubernetes API request failed")
	ErrInvalidResponse         = errors.New("invalid Agent resource response")
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

type MutationResourceRequester interface {
	RequestResourceMutation(
		ctx context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		requestBody io.Reader,
		responseBody io.Writer,
		idempotencyKey string,
	) (*agentv1.ResourceResponse, error)
}

type Service struct {
	requester      ResourceRequester
	responseBudget *semaphore.Weighted
}

type Config struct {
	MaxBufferedResponseBytes int64
}

// responseBuffer holds one Agent response while it is decoded, drawing its
// space from an instance-wide budget so that concurrent large responses cannot
// add up to unbounded Server memory.
type responseBuffer struct {
	bytes.Buffer
	budget   *semaphore.Weighted
	reserved int64
	written  int64
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
	Name              string    `json:"name"`
	UID               string    `json:"uid"`
	CreationTimestamp time.Time `json:"creation_timestamp"`
	Status            string    `json:"status"`
	Unschedulable     bool      `json:"unschedulable"`
	Roles             []string  `json:"roles"`
	InternalIP        string    `json:"internal_ip"`
	KubernetesVersion string    `json:"kubernetes_version"`
	OperatingSystem   string    `json:"operating_system"`
	OSImage           string    `json:"os_image"`
	KernelVersion     string    `json:"kernel_version"`
	ContainerRuntime  string    `json:"container_runtime"`
	CPUCapacity       string    `json:"cpu_capacity"`
	MemoryCapacity    string    `json:"memory_capacity"`
	PodsCapacity      string    `json:"pods_capacity"`
	CPUAllocatable    string    `json:"cpu_allocatable"`
	MemoryAllocatable string    `json:"memory_allocatable"`
	PodsAllocatable   string    `json:"pods_allocatable"`
}

type NodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type NodeTaint struct {
	Key       string     `json:"key"`
	Value     string     `json:"value"`
	Effect    string     `json:"effect"`
	TimeAdded *time.Time `json:"time_added,omitempty"`
}

type NodeCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastHeartbeatTime  time.Time `json:"last_heartbeat_time"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type NodeDetail struct {
	NodeSummary
	// ResourceVersion is internal concurrency identity used by aggregate reads;
	// the public Node detail contract intentionally remains unchanged.
	ResourceVersion string            `json:"-"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	ProviderID      string            `json:"provider_id"`
	PodCIDR         string            `json:"pod_cidr"`
	PodCIDRs        []string          `json:"pod_cidrs"`
	Addresses       []NodeAddress     `json:"addresses"`
	Taints          []NodeTaint       `json:"taints"`
	Conditions      []NodeCondition   `json:"conditions"`
	Architecture    string            `json:"architecture"`
	BootID          string            `json:"boot_id"`
	MachineID       string            `json:"machine_id"`
	SystemUUID      string            `json:"system_uuid"`
}

func NewService(requester ResourceRequester, configs ...Config) *Service {
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.MaxBufferedResponseBytes <= 0 {
		config.MaxBufferedResponseBytes = defaultMaxBufferedResponseBytes
	}
	return &Service{
		requester:      requester,
		responseBudget: semaphore.NewWeighted(config.MaxBufferedResponseBytes),
	}
}

func (service *Service) newResponseBuffer() *responseBuffer {
	return &responseBuffer{budget: service.responseBudget}
}

// ReserveResourceBody claims the whole declared response body before any of it
// is read, so an exhausted budget refuses this request outright instead of
// parking it — and every other request holding part of the budget — until the
// deadline expires. See agentprotocol.ResourceBodyReserver.
func (buffer *responseBuffer) ReserveResourceBody(size uint64) error {
	if size == 0 {
		return nil
	}
	if size > math.MaxInt64 ||
		!buffer.budget.TryAcquire(int64(size)) {
		return ErrResponseBudget
	}
	buffer.reserved += int64(size)
	// Reserving without pre-sizing would let the buffer's own doubling
	// transiently hold roughly twice what the budget was told about.
	if size <= math.MaxInt32 {
		buffer.Grow(int(size))
	}
	return nil
}

// Write charges anything beyond the reservation to the budget as it arrives.
// A declared body never reaches that path; a writer used without a declared
// size still cannot escape the budget.
func (buffer *responseBuffer) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	unreserved := buffer.written + int64(len(value)) - buffer.reserved
	if unreserved > 0 {
		if !buffer.budget.TryAcquire(unreserved) {
			return 0, ErrResponseBudget
		}
		buffer.reserved += unreserved
	}
	written, err := buffer.Buffer.Write(value)
	buffer.written += int64(written)
	return written, err
}

func (buffer *responseBuffer) Release() {
	if buffer == nil || buffer.reserved == 0 {
		return
	}
	buffer.budget.Release(buffer.reserved)
	buffer.reserved = 0
}

func (service *Service) ListNodes(
	ctx context.Context,
	input ListNodesInput,
) (NodePage, error) {
	if err := validateListNodesInput(input); err != nil {
		return NodePage{}, err
	}
	body := service.newResponseBuffer()
	defer body.Release()
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
		body,
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
	body := service.newResponseBuffer()
	defer body.Release()
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
		body,
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
	case errors.Is(err, ErrResponseBudget):
		// Raised by the response buffer itself while the transport reads the
		// declared body, so it travels back out through the requester.
		return ErrResponseBudget
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

// UpstreamRejection is Kubernetes refusing the object the caller submitted,
// together with the API Server's own account of why.
//
// It unwraps to ErrUpstreamRejected so callers can match it like any other
// sentinel, while the detail travels with the specific failure.
type UpstreamRejection struct {
	Message string
}

func (rejection *UpstreamRejection) Error() string {
	return ErrUpstreamRejected.Error() + ": " + rejection.Message
}

// Detail is the API Server's own account, safe to return to the caller: it
// describes the object the caller just submitted.
func (rejection *UpstreamRejection) Detail() string {
	return rejection.Message
}

func (rejection *UpstreamRejection) Unwrap() error {
	return ErrUpstreamRejected
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
		// `Invalid` is the API Server refusing the submitted object and saying
		// which field it refused. That explanation is carried through; a request
		// this Server itself found malformed never reaches an Agent, so nothing
		// here is a guess about what the caller meant.
		if response.GetReason() == "Invalid" && response.GetMessage() != "" {
			return &UpstreamRejection{Message: response.GetMessage()}
		}
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		// The Agent names its own refusals, and only its own: a 403 relayed from
		// the Kubernetes API Server carries that API's reasons, none of which
		// are these. Matching on the name keeps "ZKE will never do this" apart
		// from "this Agent has not been granted that yet", which are different
		// answers to whoever is looking at the failure.
		switch response.GetReason() {
		case agentResourceNotAllowedReason:
			return ErrResourceNotEnabled
		case agentNamespaceForbiddenReason:
			return ErrAgentNamespaceForbidden
		}
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
		NodeSummary:     summarizeNode(node),
		ResourceVersion: node.ResourceVersion,
		Labels:          cloneMap(node.Labels),
		Annotations:     cloneMap(node.Annotations),
		ProviderID:      node.Spec.ProviderID,
		PodCIDR:         node.Spec.PodCIDR,
		PodCIDRs:        append(make([]string, 0, len(node.Spec.PodCIDRs)), node.Spec.PodCIDRs...),
		Addresses:       addresses,
		Taints:          taints,
		Conditions:      conditions,
		Architecture:    node.Status.NodeInfo.Architecture,
		BootID:          node.Status.NodeInfo.BootID,
		MachineID:       node.Status.NodeInfo.MachineID,
		SystemUUID:      node.Status.NodeInfo.SystemUUID,
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
