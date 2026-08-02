package clusteroverview

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	resourcehelper "k8s.io/component-helpers/resource"
)

const (
	defaultPageSize           int64 = 500
	defaultMaxItemsPerSection int   = 10_000
	defaultMaxParallelQueries int   = 4
)

type ResourceReader interface {
	ListNodes(context.Context, kubernetesresource.ListNodesInput) (kubernetesresource.NodePage, error)
	ListResources(context.Context, kubernetesresource.ListResourcesInput) (kubernetesresource.ResourcePage, error)
	ListWorkloads(context.Context, kubernetesresource.ListWorkloadsInput) (kubernetesresource.WorkloadPage, error)
}

type Config struct {
	PageSize           int64
	MaxItemsPerSection int
	MaxParallelQueries int
}

type Service struct {
	resources ResourceReader
	config    Config
}

type Overview struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Partial     bool                  `json:"partial"`
	Issues      []SectionIssue        `json:"issues"`
	Nodes       NodeOverview          `json:"nodes"`
	Namespaces  NamespaceOverview     `json:"namespaces"`
	Pods        PodOverview           `json:"pods"`
	Workloads   WorkloadOverview      `json:"workloads"`
	Resources   ClusterResourceTotals `json:"resources"`
}

type SectionIssue struct {
	Section string `json:"section"`
	Code    string `json:"code"`
}

type NodeOverview struct {
	Total         int64            `json:"total"`
	Unschedulable int64            `json:"unschedulable"`
	StatusCounts  map[string]int64 `json:"status_counts"`
}

type NamespaceOverview struct {
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type PodOverview struct {
	Total        int64            `json:"total"`
	Ready        int64            `json:"ready"`
	NotReady     int64            `json:"not_ready"`
	Terminating  int64            `json:"terminating"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type WorkloadOverview struct {
	Total        int64                      `json:"total"`
	StatusCounts map[string]int64           `json:"status_counts"`
	ByResource   []WorkloadResourceOverview `json:"by_resource"`
}

type WorkloadResourceOverview struct {
	Resource     string           `json:"resource"`
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type ClusterResourceTotals struct {
	CPUCapacityMillis      int64 `json:"cpu_capacity_millis"`
	CPUAllocatableMillis   int64 `json:"cpu_allocatable_millis"`
	CPURequestedMillis     int64 `json:"cpu_requested_millis"`
	MemoryCapacityBytes    int64 `json:"memory_capacity_bytes"`
	MemoryAllocatableBytes int64 `json:"memory_allocatable_bytes"`
	MemoryRequestedBytes   int64 `json:"memory_requested_bytes"`
	PodCapacity            int64 `json:"pod_capacity"`
	PodAllocatable         int64 `json:"pod_allocatable"`
	NonTerminalPods        int64 `json:"non_terminal_pods"`
}

type sectionResult struct {
	section   string
	value     any
	truncated bool
	err       error
}

type nodeSnapshot struct {
	overview  NodeOverview
	resources ClusterResourceTotals
}

type podSnapshot struct {
	overview  PodOverview
	resources ClusterResourceTotals
}

type sectionTask struct {
	section string
	load    func(context.Context) (any, bool, error)
}

func NewService(resources ResourceReader, configs ...Config) *Service {
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.PageSize <= 0 || config.PageSize > kubernetesresource.MaxResourceListLimit {
		config.PageSize = defaultPageSize
	}
	if config.MaxItemsPerSection <= 0 {
		config.MaxItemsPerSection = defaultMaxItemsPerSection
	}
	if config.MaxParallelQueries <= 0 {
		config.MaxParallelQueries = defaultMaxParallelQueries
	}
	return &Service{resources: resources, config: config}
}

func (service *Service) Get(ctx context.Context, clusterID string) (Overview, error) {
	if service == nil || service.resources == nil || ctx == nil || !validation.IsUUID(clusterID) {
		return Overview{}, kubernetesresource.ErrInvalidInput
	}
	tasks := service.tasks(clusterID)
	results := make([]sectionResult, len(tasks))
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(service.config.MaxParallelQueries)
	for index := range tasks {
		index := index
		group.Go(func() error {
			value, truncated, err := tasks[index].load(groupContext)
			results[index] = sectionResult{
				section: tasks[index].section, value: value, truncated: truncated, err: err,
			}
			// Section failures are intentionally collected instead of canceling
			// sibling queries: a useful overview may still be returned as partial.
			return nil
		})
	}
	_ = group.Wait()

	overview := Overview{
		GeneratedAt: time.Now().UTC(),
		Issues:      make([]SectionIssue, 0),
		Nodes:       NodeOverview{StatusCounts: map[string]int64{}},
		Namespaces:  NamespaceOverview{StatusCounts: map[string]int64{}},
		Pods:        PodOverview{StatusCounts: map[string]int64{}},
		Workloads: WorkloadOverview{
			StatusCounts: map[string]int64{},
			ByResource:   make([]WorkloadResourceOverview, 0, 5),
		},
	}
	succeeded := 0
	var firstError error
	for _, result := range results {
		if result.err != nil {
			if firstError == nil {
				firstError = result.err
			}
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: result.section,
				Code:    issueCode(result.err),
			})
			continue
		}
		succeeded++
		if result.truncated {
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: result.section,
				Code:    "item_limit_reached",
			})
		}
		switch value := result.value.(type) {
		case nodeSnapshot:
			overview.Nodes = value.overview
			mergeNodeResources(&overview.Resources, value.resources)
		case NamespaceOverview:
			overview.Namespaces = value
		case podSnapshot:
			overview.Pods = value.overview
			mergePodResources(&overview.Resources, value.resources)
		case WorkloadResourceOverview:
			overview.Workloads.ByResource = append(overview.Workloads.ByResource, value)
			overview.Workloads.Total += value.Total
			mergeStatusCounts(overview.Workloads.StatusCounts, value.StatusCounts)
		}
	}
	if succeeded == 0 {
		if firstError == nil {
			firstError = kubernetesresource.ErrUpstreamFailure
		}
		return Overview{}, firstError
	}
	overview.Partial = len(overview.Issues) > 0
	sort.Slice(overview.Workloads.ByResource, func(left, right int) bool {
		return overview.Workloads.ByResource[left].Resource < overview.Workloads.ByResource[right].Resource
	})
	return overview, nil
}

func (service *Service) tasks(clusterID string) []sectionTask {
	tasks := []sectionTask{
		{section: "nodes", load: func(ctx context.Context) (any, bool, error) {
			return service.loadNodes(ctx, clusterID)
		}},
		{section: "namespaces", load: func(ctx context.Context) (any, bool, error) {
			return service.loadNamespaces(ctx, clusterID)
		}},
		{section: "pods", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPods(ctx, clusterID)
		}},
	}
	for _, resourceName := range []kubernetesresource.WorkloadResource{
		kubernetesresource.WorkloadDeployments,
		kubernetesresource.WorkloadStatefulSets,
		kubernetesresource.WorkloadDaemonSets,
		kubernetesresource.WorkloadJobs,
		kubernetesresource.WorkloadCronJobs,
	} {
		resourceName := resourceName
		tasks = append(tasks, sectionTask{
			section: "workloads." + string(resourceName),
			load: func(ctx context.Context) (any, bool, error) {
				return service.loadWorkloads(ctx, clusterID, resourceName)
			},
		})
	}
	return tasks
}

func (service *Service) loadNodes(ctx context.Context, clusterID string) (any, bool, error) {
	snapshot := nodeSnapshot{overview: NodeOverview{StatusCounts: map[string]int64{}}}
	continuation := ""
	for {
		page, err := service.resources.ListNodes(ctx, kubernetesresource.ListNodesInput{
			ClusterID: clusterID, Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, node := range page.Nodes {
			if snapshot.overview.Total >= int64(service.config.MaxItemsPerSection) {
				return snapshot, true, nil
			}
			snapshot.overview.Total++
			incrementStatus(snapshot.overview.StatusCounts, node.Status)
			if node.Unschedulable {
				snapshot.overview.Unschedulable++
			}
			if err := addNodeResources(&snapshot.resources, node); err != nil {
				return nil, false, err
			}
		}
		if page.ContinueToken == "" {
			return snapshot, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadNamespaces(ctx context.Context, clusterID string) (any, bool, error) {
	overview := NamespaceOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "namespaces"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var namespace corev1.Namespace
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &namespace) != nil ||
				namespace.APIVersion != "v1" || namespace.Kind != "Namespace" || namespace.Name == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			status := strings.ToLower(string(namespace.Status.Phase))
			if namespace.DeletionTimestamp != nil {
				status = strings.ToLower(string(corev1.NamespaceTerminating))
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, status)
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadPods(ctx context.Context, clusterID string) (any, bool, error) {
	snapshot := podSnapshot{overview: PodOverview{StatusCounts: map[string]int64{}}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "pods"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if snapshot.overview.Total >= int64(service.config.MaxItemsPerSection) {
				return snapshot, true, nil
			}
			var pod corev1.Pod
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &pod) != nil ||
				pod.APIVersion != "v1" || pod.Kind != "Pod" || pod.Name == "" || pod.Namespace == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			snapshot.overview.Total++
			incrementStatus(snapshot.overview.StatusCounts, strings.ToLower(string(pod.Status.Phase)))
			terminal := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
			if pod.DeletionTimestamp != nil {
				snapshot.overview.Terminating++
			}
			if podReady(&pod) {
				snapshot.overview.Ready++
			} else if !terminal {
				snapshot.overview.NotReady++
			}
			if !terminal {
				if err := addPodRequests(&snapshot.resources, &pod); err != nil {
					return nil, false, err
				}
			}
		}
		if page.ContinueToken == "" {
			return snapshot, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadWorkloads(
	ctx context.Context,
	clusterID string,
	resourceName kubernetesresource.WorkloadResource,
) (any, bool, error) {
	overview := WorkloadResourceOverview{
		Resource: string(resourceName), StatusCounts: map[string]int64{},
	}
	continuation := ""
	for {
		page, err := service.resources.ListWorkloads(ctx, kubernetesresource.ListWorkloadsInput{
			ClusterID: clusterID, Resource: resourceName,
			Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, workload := range page.Workloads {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, workload.Status)
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func addNodeResources(target *ClusterResourceTotals, node kubernetesresource.NodeSummary) error {
	values := []struct {
		text string
		cpu  bool
		dest *int64
	}{
		{node.CPUCapacity, true, &target.CPUCapacityMillis},
		{node.CPUAllocatable, true, &target.CPUAllocatableMillis},
		{node.MemoryCapacity, false, &target.MemoryCapacityBytes},
		{node.MemoryAllocatable, false, &target.MemoryAllocatableBytes},
		{node.PodsCapacity, false, &target.PodCapacity},
		{node.PodsAllocatable, false, &target.PodAllocatable},
	}
	for _, value := range values {
		parsed, err := quantityValue(value.text, value.cpu)
		if err != nil || !checkedAdd(value.dest, parsed) {
			return kubernetesresource.ErrInvalidResponse
		}
	}
	return nil
}

func addPodRequests(target *ClusterResourceTotals, pod *corev1.Pod) error {
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	cpu := requests[corev1.ResourceCPU]
	memory := requests[corev1.ResourceMemory]
	if cpu.Sign() < 0 || memory.Sign() < 0 ||
		!checkedAdd(&target.CPURequestedMillis, cpu.MilliValue()) ||
		!checkedAdd(&target.MemoryRequestedBytes, memory.Value()) ||
		!checkedAdd(&target.NonTerminalPods, 1) {
		return kubernetesresource.ErrInvalidResponse
	}
	return nil
}

func quantityValue(text string, cpu bool) (int64, error) {
	if text == "" {
		return 0, nil
	}
	quantity, err := resource.ParseQuantity(text)
	if err != nil || quantity.Sign() < 0 {
		return 0, kubernetesresource.ErrInvalidResponse
	}
	if cpu {
		return quantity.MilliValue(), nil
	}
	return quantity.Value(), nil
}

func checkedAdd(target *int64, value int64) bool {
	if target == nil || value < 0 || *target > math.MaxInt64-value {
		return false
	}
	*target += value
	return true
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func incrementStatus(counts map[string]int64, status string) {
	if status == "" {
		status = "unknown"
	}
	counts[status]++
}

func mergeStatusCounts(target map[string]int64, source map[string]int64) {
	for status, count := range source {
		target[status] += count
	}
}

func mergeNodeResources(target *ClusterResourceTotals, source ClusterResourceTotals) {
	target.CPUCapacityMillis = source.CPUCapacityMillis
	target.CPUAllocatableMillis = source.CPUAllocatableMillis
	target.MemoryCapacityBytes = source.MemoryCapacityBytes
	target.MemoryAllocatableBytes = source.MemoryAllocatableBytes
	target.PodCapacity = source.PodCapacity
	target.PodAllocatable = source.PodAllocatable
}

func mergePodResources(target *ClusterResourceTotals, source ClusterResourceTotals) {
	target.CPURequestedMillis = source.CPURequestedMillis
	target.MemoryRequestedBytes = source.MemoryRequestedBytes
	target.NonTerminalPods = source.NonTerminalPods
}

func issueCode(err error) string {
	switch {
	case errors.Is(err, kubernetesresource.ErrAgentNotConnected):
		return "agent_not_connected"
	case errors.Is(err, kubernetesresource.ErrAgentUnsupported):
		return "agent_capability_unavailable"
	case errors.Is(err, kubernetesresource.ErrRequestCapacity):
		return "resource_capacity_exhausted"
	case errors.Is(err, kubernetesresource.ErrResponseBudget):
		return "response_budget_exhausted"
	case errors.Is(err, kubernetesresource.ErrClusterUnauthenticated):
		return "cluster_api_unauthenticated"
	case errors.Is(err, kubernetesresource.ErrClusterAccessDenied):
		return "cluster_api_forbidden"
	case errors.Is(err, kubernetesresource.ErrClusterUnavailable):
		return "cluster_api_unavailable"
	case errors.Is(err, kubernetesresource.ErrClusterTimeout), errors.Is(err, context.DeadlineExceeded):
		return "cluster_api_timeout"
	case errors.Is(err, kubernetesresource.ErrResponseTooLarge):
		return "agent_response_too_large"
	case errors.Is(err, kubernetesresource.ErrInvalidResponse):
		return "invalid_agent_response"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "cluster_api_error"
	}
}
