package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type kubernetesNodeService interface {
	ListNodes(
		context.Context,
		kubernetesresource.ListNodesInput,
	) (kubernetesresource.NodePage, error)
	GetNode(
		context.Context,
		string,
		string,
	) (kubernetesresource.NodeDetail, error)
	DrainNode(
		context.Context,
		kubernetesresource.DrainNodeInput,
	) (kubernetesresource.DrainNodeResult, error)
}

type kubernetesNodeHandler struct {
	baseHandler
	service kubernetesNodeService
}

type nodeSummaryResponse struct {
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

type nodeAddressResponse struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type nodeTaintResponse struct {
	Key       string     `json:"key"`
	Value     string     `json:"value"`
	Effect    string     `json:"effect"`
	TimeAdded *time.Time `json:"time_added,omitempty"`
}

type nodeConditionResponse struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastHeartbeatTime  time.Time `json:"last_heartbeat_time"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type nodeDetailResponse struct {
	nodeSummaryResponse
	Labels       map[string]string       `json:"labels"`
	Annotations  map[string]string       `json:"annotations"`
	ProviderID   string                  `json:"provider_id"`
	PodCIDR      string                  `json:"pod_cidr"`
	PodCIDRs     []string                `json:"pod_cidrs"`
	Addresses    []nodeAddressResponse   `json:"addresses"`
	Taints       []nodeTaintResponse     `json:"taints"`
	Conditions   []nodeConditionResponse `json:"conditions"`
	Architecture string                  `json:"architecture"`
	BootID       string                  `json:"boot_id"`
	MachineID    string                  `json:"machine_id"`
	SystemUUID   string                  `json:"system_uuid"`
}

func newKubernetesNodeHandler(
	logger *slog.Logger,
	service kubernetesNodeService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesNodeHandler {
	return &kubernetesNodeHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

const maxNodeDrainRequestBytes = 4096

type nodeDrainRequest struct {
	UID                string `json:"uid"`
	DryRun             bool   `json:"dry_run"`
	Confirm            bool   `json:"confirm"`
	ForceUnmanaged     bool   `json:"force_unmanaged"`
	DeleteEmptyDirData bool   `json:"delete_empty_dir_data"`
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
}

func (handler *kubernetesNodeHandler) drain(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	request := nodeDrainRequest{}
	target := "core/v1/nodes name:" + c.Param("node_name")
	if decodeJSONRequest(c, &request, maxNodeDrainRequestBytes) != nil {
		handler.recordDrain(c, identity.User.ID, auditaction.KubernetesNodeDrain, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Node drain request")
		return
	}
	action := auditaction.KubernetesNodeDrain
	if request.DryRun {
		action = auditaction.KubernetesNodeDrainDryRun
	}
	target += " uid:" + request.UID
	if handler.service == nil {
		handler.recordDrain(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Node drain is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	protectedGrant := httpmiddleware.ClusterProtectedNamespaceGrant(c)
	resolvedScope, _ := httpmiddleware.ResolvedScope(c)
	result, err := handler.service.DrainNode(ctx, kubernetesresource.DrainNodeInput{
		ClusterID:             c.Param("cluster_id"),
		NodeName:              c.Param("node_name"),
		NodeUID:               request.UID,
		DryRun:                request.DryRun,
		Confirm:               request.Confirm,
		ForceUnmanaged:        request.ForceUnmanaged,
		DeleteEmptyDirData:    request.DeleteEmptyDirData,
		GracePeriodSeconds:    request.GracePeriodSeconds,
		IdempotencyKey:        c.GetHeader(idempotencyKeyHeaderName),
		SystemNamespaceManage: protectedGrant.System,
		AgentNamespaceManage:  protectedGrant.Agent,
		AgentNamespace:        resolvedScope.AgentNamespace,
	})
	cancel()
	if err != nil {
		handler.recordDrain(c, identity.User.ID, action, target, "failed")
		handler.respondNodeError(c, "drain Kubernetes Node", err)
		return
	}
	auditResult := "succeeded"
	if !request.DryRun && !drainCompleted(result) {
		auditResult = "failed"
	}
	handler.recordDrain(c, identity.User.ID, action, target, auditResult)
	writeSuccess(c, http.StatusOK, result)
}

func drainCompleted(result kubernetesresource.DrainNodeResult) bool {
	if result.Blocked {
		return false
	}
	for _, pod := range result.Pods {
		if pod.Result == kubernetesresource.DrainPodFailed ||
			pod.Result == kubernetesresource.DrainPodPDBBlocked {
			return false
		}
	}
	return true
}

func (handler *kubernetesNodeHandler) recordDrain(c *gin.Context, actor, action, target, result string) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actor,
		Action:      action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  target,
		Result:      result,
	})
}

func (handler *kubernetesNodeHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseNodeListQuery(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Node query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Node query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListNodes(ctx, input)
	cancel()
	if handler.respondNodeError(c, "list Kubernetes Nodes", err) {
		return
	}
	nodes := make([]nodeSummaryResponse, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes = append(nodes, responseNodeSummary(node))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"nodes":                nodes,
		"continue_token":       result.ContinueToken,
		"resource_version":     result.ResourceVersion,
		"remaining_item_count": result.RemainingItemCount,
	})
}

func (handler *kubernetesNodeHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Node detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Node query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetNode(
		ctx,
		c.Param("cluster_id"),
		c.Param("node_name"),
	)
	cancel()
	if handler.respondNodeError(c, "get Kubernetes Node", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseNodeDetail(result))
}

func parseNodeListQuery(c *gin.Context) (kubernetesresource.ListNodesInput, error) {
	query := c.Request.URL.Query()
	for name, values := range query {
		switch name {
		case "limit", "continue", "label_selector", "field_selector":
		default:
			return kubernetesresource.ListNodesInput{}, errors.New("unsupported Node query parameter")
		}
		if len(values) != 1 {
			return kubernetesresource.ListNodesInput{}, errors.New("duplicate Node query parameter")
		}
	}
	result := kubernetesresource.ListNodesInput{
		Limit:         kubernetesresource.DefaultNodeListLimit,
		ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"),
		FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListNodesInput{}, errors.New("invalid Node list limit")
		}
		result.Limit = limit
	}
	return result, nil
}

func (handler *kubernetesNodeHandler) respondNodeError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrRequestCapacity) ||
		errors.Is(err, kubernetesresource.ErrResponseBudget) {
		c.Header("Retry-After", "1")
	}
	return handler.respondError(
		c,
		operation,
		err,
		errorMapping{kubernetesresource.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Node request"},
		errorMapping{kubernetesresource.ErrNodeNotFound, http.StatusNotFound, "node_not_found", "Node not found"},
		errorMapping{kubernetesresource.ErrDrainInventoryTruncated, http.StatusConflict, "drain_inventory_truncated", "Node has more Pods than the bounded drain inventory can safely process"},
		errorMapping{kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		errorMapping{kubernetesresource.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support resource queries"},
		errorMapping{kubernetesresource.ErrRequestCapacity, http.StatusTooManyRequests, "resource_capacity_exhausted", "resource query capacity is exhausted"},
		errorMapping{kubernetesresource.ErrResponseBudget, http.StatusTooManyRequests, "response_budget_exhausted", "Server response buffer budget is exhausted"},
		errorMapping{kubernetesresource.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		errorMapping{kubernetesresource.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes API request timed out"},
		errorMapping{kubernetesresource.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		errorMapping{kubernetesresource.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read Nodes"},
		errorMapping{kubernetesresource.ErrResponseTooLarge, http.StatusBadGateway, "agent_response_too_large", "Agent response exceeded the configured limit"},
		errorMapping{kubernetesresource.ErrUpstreamConflict, http.StatusConflict, "cluster_api_conflict", "Kubernetes resource changed during the request"},
		errorMapping{kubernetesresource.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid resource response"},
		errorMapping{kubernetesresource.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes resource query failed"},
	)
}

func responseNodeSummary(node kubernetesresource.NodeSummary) nodeSummaryResponse {
	return nodeSummaryResponse{
		Name:              node.Name,
		UID:               node.UID,
		CreationTimestamp: responseTime(node.CreationTimestamp),
		Status:            node.Status,
		Unschedulable:     node.Unschedulable,
		Roles:             cloneStringSlice(node.Roles),
		InternalIP:        node.InternalIP,
		KubernetesVersion: node.KubernetesVersion,
		OperatingSystem:   node.OperatingSystem,
		OSImage:           node.OSImage,
		KernelVersion:     node.KernelVersion,
		ContainerRuntime:  node.ContainerRuntime,
		CPUCapacity:       node.CPUCapacity,
		MemoryCapacity:    node.MemoryCapacity,
		PodsCapacity:      node.PodsCapacity,
		CPUAllocatable:    node.CPUAllocatable,
		MemoryAllocatable: node.MemoryAllocatable,
		PodsAllocatable:   node.PodsAllocatable,
	}
}

func responseNodeDetail(node kubernetesresource.NodeDetail) nodeDetailResponse {
	addresses := make([]nodeAddressResponse, 0, len(node.Addresses))
	for _, address := range node.Addresses {
		addresses = append(addresses, nodeAddressResponse(address))
	}
	taints := make([]nodeTaintResponse, 0, len(node.Taints))
	for _, taint := range node.Taints {
		taints = append(taints, nodeTaintResponse{
			Key:       taint.Key,
			Value:     taint.Value,
			Effect:    taint.Effect,
			TimeAdded: responseTimePointer(taint.TimeAdded),
		})
	}
	conditions := make([]nodeConditionResponse, 0, len(node.Conditions))
	for _, condition := range node.Conditions {
		conditions = append(conditions, nodeConditionResponse{
			Type:               condition.Type,
			Status:             condition.Status,
			Reason:             condition.Reason,
			Message:            condition.Message,
			LastHeartbeatTime:  responseTime(condition.LastHeartbeatTime),
			LastTransitionTime: responseTime(condition.LastTransitionTime),
		})
	}
	return nodeDetailResponse{
		nodeSummaryResponse: responseNodeSummary(node.NodeSummary),
		Labels:              cloneStringMap(node.Labels),
		Annotations:         cloneStringMap(node.Annotations),
		ProviderID:          node.ProviderID,
		PodCIDR:             node.PodCIDR,
		PodCIDRs:            cloneStringSlice(node.PodCIDRs),
		Addresses:           addresses,
		Taints:              taints,
		Conditions:          conditions,
		Architecture:        node.Architecture,
		BootID:              node.BootID,
		MachineID:           node.MachineID,
		SystemUUID:          node.SystemUUID,
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneStringSlice(input []string) []string {
	result := make([]string, len(input))
	copy(result, input)
	return result
}
