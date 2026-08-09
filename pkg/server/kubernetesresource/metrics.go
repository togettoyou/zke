package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	MetricsUnavailableNotInstalled = "metrics_api_not_installed"
	MetricsUnavailableNotReady     = "metrics_api_unavailable"
)

var (
	errMetricsAPIUnavailable = errors.New("Kubernetes metrics API is unavailable")
	nodeMetricsResource      = &agentv1.GroupVersionResource{
		Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes",
	}
	podMetricsResource = &agentv1.GroupVersionResource{
		Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods",
	}
)

type MetricsAvailability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
}

type NodeMetricsSnapshot struct {
	MetricsAvailability
	GeneratedAt time.Time    `json:"generated_at"`
	Items       []NodeMetric `json:"items"`
}

type NodeMetric struct {
	Name             string    `json:"name"`
	Timestamp        time.Time `json:"timestamp"`
	WindowSeconds    int64     `json:"window_seconds"`
	CPUUsageMillis   int64     `json:"cpu_usage_millis"`
	MemoryUsageBytes int64     `json:"memory_usage_bytes"`
}

type PodMetricsSnapshot struct {
	MetricsAvailability
	GeneratedAt time.Time   `json:"generated_at"`
	Items       []PodMetric `json:"items"`
}

type PodMetric struct {
	Name             string    `json:"name"`
	Timestamp        time.Time `json:"timestamp"`
	WindowSeconds    int64     `json:"window_seconds"`
	ContainerCount   int64     `json:"container_count"`
	CPUUsageMillis   int64     `json:"cpu_usage_millis"`
	MemoryUsageBytes int64     `json:"memory_usage_bytes"`
}

type rawMetricsList struct {
	APIVersion string          `json:"apiVersion"`
	Items      json.RawMessage `json:"items"`
}

type rawMetricMetadata struct {
	Name string `json:"name"`
}

type rawNodeMetric struct {
	Metadata  rawMetricMetadata               `json:"metadata"`
	Timestamp time.Time                       `json:"timestamp"`
	Window    string                          `json:"window"`
	Usage     map[string]apiresource.Quantity `json:"usage"`
}

type rawPodMetric struct {
	Metadata   rawMetricMetadata `json:"metadata"`
	Timestamp  time.Time         `json:"timestamp"`
	Window     string            `json:"window"`
	Containers []struct {
		Name  string                          `json:"name"`
		Usage map[string]apiresource.Quantity `json:"usage"`
	} `json:"containers"`
}

func (service *Service) ListNodeMetrics(
	ctx context.Context,
	clusterID string,
) (NodeMetricsSnapshot, error) {
	if !validation.IsUUID(clusterID) {
		return NodeMetricsSnapshot{}, ErrInvalidInput
	}
	generatedAt := time.Now().UTC()
	body, err := service.listMetrics(ctx, clusterID, "", nodeMetricsResource)
	if availability, ok := unavailableMetrics(err); ok {
		return NodeMetricsSnapshot{
			MetricsAvailability: availability,
			GeneratedAt:         generatedAt,
			Items:               []NodeMetric{},
		}, nil
	}
	if err != nil {
		return NodeMetricsSnapshot{}, err
	}
	items, err := decodeNodeMetrics(body)
	if err != nil {
		return NodeMetricsSnapshot{}, err
	}
	return NodeMetricsSnapshot{
		MetricsAvailability: MetricsAvailability{Available: true},
		GeneratedAt:         generatedAt,
		Items:               items,
	}, nil
}

func (service *Service) ListPodMetrics(
	ctx context.Context,
	clusterID string,
	namespace string,
) (PodMetricsSnapshot, error) {
	if !validation.IsUUID(clusterID) ||
		len(k8svalidation.IsDNS1123Label(namespace)) != 0 {
		return PodMetricsSnapshot{}, ErrInvalidInput
	}
	generatedAt := time.Now().UTC()
	body, err := service.listMetrics(ctx, clusterID, namespace, podMetricsResource)
	if availability, ok := unavailableMetrics(err); ok {
		return PodMetricsSnapshot{
			MetricsAvailability: availability,
			GeneratedAt:         generatedAt,
			Items:               []PodMetric{},
		}, nil
	}
	if err != nil {
		return PodMetricsSnapshot{}, err
	}
	items, err := decodePodMetrics(body)
	if err != nil {
		return PodMetricsSnapshot{}, err
	}
	return PodMetricsSnapshot{
		MetricsAvailability: MetricsAvailability{Available: true},
		GeneratedAt:         generatedAt,
		Items:               items,
	}, nil
}

func (service *Service) listMetrics(
	ctx context.Context,
	clusterID string,
	namespace string,
	resource *agentv1.GroupVersionResource,
) ([]byte, error) {
	body := service.newResponseBuffer()
	defer body.Release()
	response, err := service.requester.RequestResource(
		ctx,
		clusterID,
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource:       resource,
			Namespace:      namespace,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
		body,
	)
	if err != nil {
		return nil, requestError(err)
	}
	// Discovery has already confirmed that this optional API exists. Only a
	// ServiceUnavailable returned by the metrics endpoint itself is a Metrics
	// Server availability state; DiscoveryFailed or another generic Kubernetes
	// outage must keep travelling as a real request error.
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNAVAILABLE &&
		response.GetReason() == "ServiceUnavailable" {
		return nil, errMetricsAPIUnavailable
	}
	if err := responseErrorWithNotFound(response, ErrResourceNotFound); err != nil {
		return nil, err
	}
	return append([]byte(nil), body.Bytes()...), nil
}

func unavailableMetrics(err error) (MetricsAvailability, bool) {
	switch {
	case errors.Is(err, ErrResourceNotEnabled), errors.Is(err, ErrResourceNotFound):
		return MetricsAvailability{
			Reason:  MetricsUnavailableNotInstalled,
			Message: "集群未提供 metrics.k8s.io API，请安装并配置 Metrics Server。",
		}, true
	case errors.Is(err, errMetricsAPIUnavailable):
		return MetricsAvailability{
			Reason:  MetricsUnavailableNotReady,
			Message: "metrics.k8s.io API 暂不可用，请检查 Metrics Server 状态和 APIService。",
		}, true
	default:
		return MetricsAvailability{}, false
	}
}

func decodeNodeMetrics(body []byte) ([]NodeMetric, error) {
	var list rawMetricsList
	if err := json.Unmarshal(body, &list); err != nil ||
		list.APIVersion != "metrics.k8s.io/v1beta1" || list.Items == nil {
		return nil, fmt.Errorf("%w: decode NodeMetrics list", ErrInvalidResponse)
	}
	var raw []rawNodeMetric
	if err := json.Unmarshal(list.Items, &raw); err != nil {
		return nil, fmt.Errorf("%w: decode NodeMetrics items", ErrInvalidResponse)
	}
	items := make([]NodeMetric, 0, len(raw))
	for _, metric := range raw {
		window, cpu, memory, ok := metricValues(
			metric.Metadata.Name, metric.Timestamp, metric.Window, metric.Usage,
		)
		if !ok {
			return nil, fmt.Errorf("%w: invalid NodeMetrics item", ErrInvalidResponse)
		}
		items = append(items, NodeMetric{
			Name: metric.Metadata.Name, Timestamp: metric.Timestamp,
			WindowSeconds: window, CPUUsageMillis: cpu, MemoryUsageBytes: memory,
		})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Name < items[right].Name })
	return items, nil
}

func decodePodMetrics(body []byte) ([]PodMetric, error) {
	var list rawMetricsList
	if err := json.Unmarshal(body, &list); err != nil ||
		list.APIVersion != "metrics.k8s.io/v1beta1" || list.Items == nil {
		return nil, fmt.Errorf("%w: decode PodMetrics list", ErrInvalidResponse)
	}
	var raw []rawPodMetric
	if err := json.Unmarshal(list.Items, &raw); err != nil {
		return nil, fmt.Errorf("%w: decode PodMetrics items", ErrInvalidResponse)
	}
	items := make([]PodMetric, 0, len(raw))
	for _, metric := range raw {
		if metric.Metadata.Name == "" || metric.Timestamp.IsZero() || len(metric.Containers) == 0 {
			return nil, fmt.Errorf("%w: invalid PodMetrics item", ErrInvalidResponse)
		}
		windowDuration, err := time.ParseDuration(metric.Window)
		if err != nil || windowDuration <= 0 {
			return nil, fmt.Errorf("%w: invalid PodMetrics window", ErrInvalidResponse)
		}
		var cpuTotal, memoryTotal apiresource.Quantity
		for _, container := range metric.Containers {
			cpu, cpuOK := container.Usage["cpu"]
			memory, memoryOK := container.Usage["memory"]
			if container.Name == "" || !cpuOK || !memoryOK ||
				cpu.Sign() < 0 || memory.Sign() < 0 {
				return nil, fmt.Errorf("%w: invalid PodMetrics container", ErrInvalidResponse)
			}
			cpuTotal.Add(cpu)
			memoryTotal.Add(memory)
		}
		cpu := cpuTotal.MilliValue()
		memory := memoryTotal.Value()
		if cpu < 0 || memory < 0 {
			return nil, fmt.Errorf("%w: PodMetrics usage overflow", ErrInvalidResponse)
		}
		items = append(items, PodMetric{
			Name: metric.Metadata.Name, Timestamp: metric.Timestamp,
			WindowSeconds:  int64(windowDuration / time.Second),
			ContainerCount: int64(len(metric.Containers)),
			CPUUsageMillis: cpu, MemoryUsageBytes: memory,
		})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Name < items[right].Name })
	return items, nil
}

func metricValues(
	name string,
	timestamp time.Time,
	window string,
	usage map[string]apiresource.Quantity,
) (int64, int64, int64, bool) {
	duration, err := time.ParseDuration(window)
	cpu, cpuOK := usage["cpu"]
	memory, memoryOK := usage["memory"]
	if name == "" || timestamp.IsZero() || err != nil || duration <= 0 ||
		!cpuOK || !memoryOK || cpu.Sign() < 0 || memory.Sign() < 0 {
		return 0, 0, 0, false
	}
	return int64(duration / time.Second), cpu.MilliValue(), memory.Value(), true
}
