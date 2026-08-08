package clusteroverview

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
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
	// How long a completed snapshot answers for its Cluster.
	//
	// One overview is ten full listings of the Cluster, so the page that every
	// operator lands on is also the most expensive read in this application. The
	// window is short on purpose: it absorbs bursts — several operators opening
	// the application, one operator moving between sections — without making a
	// deliberate refresh return yesterday's numbers. The response carries
	// `generated_at`, so a repeat inside the window is visibly the same snapshot
	// rather than a silently stale one.
	defaultCacheTTL = 15 * time.Second
	// Cached Clusters, so the cache cannot grow with the number of Clusters a
	// Server has ever been asked about.
	defaultMaxCachedClusters = 64
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
	// Negative disables the cache; zero selects the default.
	CacheTTL          time.Duration
	MaxCachedClusters int
}

type Service struct {
	resources ResourceReader
	config    Config
	// Completed snapshots, keyed by Cluster. Nothing here is caller-specific:
	// the overview describes a Cluster, and every caller reaching this service
	// has already been checked for `cluster.read` on that Cluster.
	mutex sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	overview  Overview
	expiresAt time.Time
}

type Overview struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Partial     bool                  `json:"partial"`
	Issues      []SectionIssue        `json:"issues"`
	Nodes       NodeOverview          `json:"nodes"`
	Namespaces  NamespaceOverview     `json:"namespaces"`
	Pods        PodOverview           `json:"pods"`
	Workloads   WorkloadOverview      `json:"workloads"`
	Storage     StorageOverview       `json:"storage"`
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
	// Nodes per reported kubelet version. A Cluster mid-upgrade reports more
	// than one, and the skew between them decides which APIs are safe to use.
	KubernetesVersions map[string]int64 `json:"kubernetes_versions"`
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

// StorageOverview reports persistent storage as counts, not as a ratio.
//
// Volume capacity has no Cluster-wide maximum to be read against the way CPU and
// memory do: with a dynamic provisioner the supply is whatever the backing
// system will still hand out, and it is not visible from Kubernetes. So this
// section reports how much has been provisioned and asked for, and how the
// objects are distributed across their phases — a Pending claim or a Failed
// volume is the thing worth seeing here, and both are counts.
type StorageOverview struct {
	PersistentVolumes      PersistentVolumeOverview      `json:"persistent_volumes"`
	PersistentVolumeClaims PersistentVolumeClaimOverview `json:"persistent_volume_claims"`
}

type PersistentVolumeOverview struct {
	Total int64 `json:"total"`
	// Sum of `spec.capacity.storage` over every PersistentVolume, whatever phase
	// it is in: a Released volume still occupies its backing storage.
	CapacityBytes int64            `json:"capacity_bytes"`
	StatusCounts  map[string]int64 `json:"status_counts"`
}

type PersistentVolumeClaimOverview struct {
	Total int64 `json:"total"`
	// Sum of `spec.resources.requests.storage`, which is what was asked for
	// rather than what was provisioned — the same reading as the CPU and memory
	// request totals above.
	RequestedBytes int64            `json:"requested_bytes"`
	StatusCounts   map[string]int64 `json:"status_counts"`
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
	if config.CacheTTL == 0 {
		config.CacheTTL = defaultCacheTTL
	}
	if config.MaxCachedClusters <= 0 {
		config.MaxCachedClusters = defaultMaxCachedClusters
	}
	return &Service{
		resources: resources,
		config:    config,
		cache:     make(map[string]cacheEntry),
	}
}

func (service *Service) Get(ctx context.Context, clusterID string) (Overview, error) {
	if service == nil || service.resources == nil || ctx == nil || !validation.IsUUID(clusterID) {
		return Overview{}, kubernetesresource.ErrInvalidInput
	}
	if cached, ok := service.cached(clusterID); ok {
		return cached, nil
	}
	overview, err := service.load(ctx, clusterID)
	if err != nil {
		return Overview{}, err
	}
	service.store(clusterID, overview)
	return overview, nil
}

// The snapshot held for this Cluster, if one is still within its window.
func (service *Service) cached(clusterID string) (Overview, bool) {
	if service.config.CacheTTL < 0 {
		return Overview{}, false
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	entry, ok := service.cache[clusterID]
	if !ok || !time.Now().Before(entry.expiresAt) {
		return Overview{}, false
	}
	// A copy, because the caller receives maps and slices that the next caller
	// will receive as well.
	return entry.overview.clone(), true
}

// Holds a snapshot for its window, unless a section of it failed.
//
// A failure is not cached: an operator who sees a section reported as
// unavailable presses refresh precisely to find out whether it still is, and a
// cache that answers that question from the failure itself has turned the button
// off. A section that only hit its item ceiling is cached — re-reading a Cluster
// too large to count will produce the same ceiling at the same cost.
func (service *Service) store(clusterID string, overview Overview) {
	if service.config.CacheTTL < 0 || !cacheable(overview) {
		return
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if _, replacing := service.cache[clusterID]; !replacing &&
		len(service.cache) >= service.config.MaxCachedClusters {
		now := time.Now()
		for key, entry := range service.cache {
			if !now.Before(entry.expiresAt) {
				delete(service.cache, key)
			}
		}
		// Still full of live entries: this Cluster simply goes uncached rather
		// than evicting a snapshot someone else is about to read.
		if len(service.cache) >= service.config.MaxCachedClusters {
			return
		}
	}
	service.cache[clusterID] = cacheEntry{
		overview:  overview.clone(),
		expiresAt: time.Now().Add(service.config.CacheTTL),
	}
}

func cacheable(overview Overview) bool {
	for _, issue := range overview.Issues {
		if issue.Code != "item_limit_reached" {
			return false
		}
	}
	return true
}

func (service *Service) load(ctx context.Context, clusterID string) (Overview, error) {
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
		Nodes: NodeOverview{
			StatusCounts: map[string]int64{}, KubernetesVersions: map[string]int64{},
		},
		Namespaces: NamespaceOverview{StatusCounts: map[string]int64{}},
		Pods:       PodOverview{StatusCounts: map[string]int64{}},
		Workloads: WorkloadOverview{
			StatusCounts: map[string]int64{},
			ByResource:   make([]WorkloadResourceOverview, 0, 5),
		},
		Storage: StorageOverview{
			PersistentVolumes:      PersistentVolumeOverview{StatusCounts: map[string]int64{}},
			PersistentVolumeClaims: PersistentVolumeClaimOverview{StatusCounts: map[string]int64{}},
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
		case PersistentVolumeOverview:
			overview.Storage.PersistentVolumes = value
		case PersistentVolumeClaimOverview:
			overview.Storage.PersistentVolumeClaims = value
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
		{section: "storage.persistentvolumes", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPersistentVolumes(ctx, clusterID)
		}},
		{section: "storage.persistentvolumeclaims", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPersistentVolumeClaims(ctx, clusterID)
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
	snapshot := nodeSnapshot{overview: NodeOverview{
		StatusCounts: map[string]int64{}, KubernetesVersions: map[string]int64{},
	}}
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
			incrementStatus(snapshot.overview.KubernetesVersions, node.KubernetesVersion)
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

func (service *Service) loadPersistentVolumes(
	ctx context.Context,
	clusterID string,
) (any, bool, error) {
	overview := PersistentVolumeOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "persistentvolumes"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var volume corev1.PersistentVolume
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &volume) != nil ||
				volume.APIVersion != "v1" || volume.Kind != "PersistentVolume" || volume.Name == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			overview.Total++
			// The phase as `kubectl get pv` prints it, and nothing derived: a
			// volume being deleted keeps the phase that says whether anything
			// still claims it.
			incrementStatus(overview.StatusCounts, strings.ToLower(string(volume.Status.Phase)))
			capacity := volume.Spec.Capacity[corev1.ResourceStorage]
			if capacity.Sign() < 0 || !checkedAdd(&overview.CapacityBytes, capacity.Value()) {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
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

func (service *Service) loadPersistentVolumeClaims(
	ctx context.Context,
	clusterID string,
) (any, bool, error) {
	overview := PersistentVolumeClaimOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource: kubernetesresource.ResourceIdentity{
				Version: "v1", Resource: "persistentvolumeclaims",
			},
			Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var claim corev1.PersistentVolumeClaim
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &claim) != nil ||
				claim.APIVersion != "v1" || claim.Kind != "PersistentVolumeClaim" ||
				claim.Name == "" || claim.Namespace == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, strings.ToLower(string(claim.Status.Phase)))
			requested := claim.Spec.Resources.Requests[corev1.ResourceStorage]
			if requested.Sign() < 0 ||
				!checkedAdd(&overview.RequestedBytes, requested.Value()) {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
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

// A snapshot handed out separately from the one held in the cache.
//
// The same Overview is returned to every caller inside its window, and it is
// made of maps and slices. Copying them keeps one caller's response from being
// something another caller could reach.
func (overview Overview) clone() Overview {
	copied := overview
	copied.Issues = append(make([]SectionIssue, 0, len(overview.Issues)), overview.Issues...)
	copied.Nodes.StatusCounts = cloneCounts(overview.Nodes.StatusCounts)
	copied.Nodes.KubernetesVersions = cloneCounts(overview.Nodes.KubernetesVersions)
	copied.Namespaces.StatusCounts = cloneCounts(overview.Namespaces.StatusCounts)
	copied.Pods.StatusCounts = cloneCounts(overview.Pods.StatusCounts)
	copied.Workloads.StatusCounts = cloneCounts(overview.Workloads.StatusCounts)
	copied.Workloads.ByResource = make([]WorkloadResourceOverview, 0, len(overview.Workloads.ByResource))
	for _, entry := range overview.Workloads.ByResource {
		entry.StatusCounts = cloneCounts(entry.StatusCounts)
		copied.Workloads.ByResource = append(copied.Workloads.ByResource, entry)
	}
	copied.Storage.PersistentVolumes.StatusCounts = cloneCounts(
		overview.Storage.PersistentVolumes.StatusCounts,
	)
	copied.Storage.PersistentVolumeClaims.StatusCounts = cloneCounts(
		overview.Storage.PersistentVolumeClaims.StatusCounts,
	)
	return copied
}

func cloneCounts(counts map[string]int64) map[string]int64 {
	copied := make(map[string]int64, len(counts))
	for key, value := range counts {
		copied[key] = value
	}
	return copied
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
