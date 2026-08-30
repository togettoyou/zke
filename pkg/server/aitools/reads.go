package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	"github.com/togettoyou/zke/pkg/shared/observability"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (catalogue *Catalogue) clusterOverview(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	overview, err := catalogue.dependencies.Overview.Get(ctx, invocation.ClusterID)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	return airuntime.ToolResult{
		Text: catalogue.encode(overview),
		Evidence: []aisession.Evidence{
			{Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID},
		},
	}, nil
}

type apiResourcesArguments struct {
	Search string `json:"search"`
}

func (catalogue *Catalogue) listAPIResources(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments apiResourcesArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	catalog, err := catalogue.discover(ctx, invocation.ClusterID)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	search := strings.ToLower(strings.TrimSpace(arguments.Search))
	lines := make([]string, 0, len(catalog.Resources))
	for _, resource := range catalog.Resources {
		apiVersion := resource.Version
		if resource.Group != "" {
			apiVersion = resource.Group + "/" + resource.Version
		}
		row := fmt.Sprintf("%s %s namespaced=%t", apiVersion, resource.Kind, resource.Namespaced)
		if search != "" && !strings.Contains(strings.ToLower(row+" "+resource.Resource), search) {
			continue
		}
		lines = append(lines, row)
	}
	sort.Strings(lines)
	text := fmt.Sprintf("匹配 %d 个资源类型（partial=%t）：\n%s",
		len(lines), catalog.Partial, strings.Join(lines, "\n"))
	return airuntime.ToolResult{Text: text}, nil
}

type listResourcesArguments struct {
	APIVersion    string `json:"api_version"`
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"label_selector"`
	FieldSelector string `json:"field_selector"`
	Limit         int    `json:"limit"`
}

func (catalogue *Catalogue) listResources(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments listResourcesArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	identity, resource, err := catalogue.resolve(ctx, invocation.ClusterID, arguments.APIVersion, arguments.Kind)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if !resource.Namespaced && arguments.Namespace != "" {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: %s 是集群级资源，不接受 namespace", airuntime.ErrInvalidInput, arguments.Kind)
	}
	page, err := catalogue.dependencies.Resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
		ClusterID: invocation.ClusterID, Resource: identity, Namespace: arguments.Namespace,
		Limit:         int64(bound(arguments.Limit, defaultListLimit, maxListLimit)),
		LabelSelector: arguments.LabelSelector, FieldSelector: arguments.FieldSelector,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	summaries := make([]map[string]any, 0, len(page.Items))
	evidence := make([]aisession.Evidence, 0, 8)
	gvk := groupVersionKind(arguments.APIVersion, resource.Kind)
	for _, item := range page.Items {
		object := unstructured.Unstructured{Object: item}
		summaries = append(summaries, summarize(object))
		// Evidence is a handle back to a real object, and an entry may only
		// carry a bounded number of them. The first few are the ones a reader
		// follows; a hundred links to a hundred Pods is not a citation.
		if len(evidence) < 8 {
			evidence = append(evidence, aisession.Evidence{
				Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
				Namespace: object.GetNamespace(), GVK: gvk, Name: object.GetName(),
				ResourceVersion: object.GetResourceVersion(),
			})
		}
	}
	header := fmt.Sprintf("%s %s：返回 %d 个对象", arguments.APIVersion, resource.Kind, len(summaries))
	if page.ContinueToken != "" {
		header += "（还有更多，请用更精确的选择器缩小范围）"
	}
	return airuntime.ToolResult{
		Text:     header + "\n" + catalogue.encode(summaries),
		Evidence: evidence,
		Target:   &aisession.Target{Namespace: arguments.Namespace, GVK: gvk},
	}, nil
}

type objectArguments struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

func (catalogue *Catalogue) getResource(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments objectArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	identity, resource, err := catalogue.resolve(ctx, invocation.ClusterID, arguments.APIVersion, arguments.Kind)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	object, err := catalogue.dependencies.Resources.GetResource(ctx, kubernetesresource.GetResourceInput{
		ClusterID: invocation.ClusterID, Resource: identity,
		Namespace: arguments.Namespace, Name: arguments.Name,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	live := unstructured.Unstructured{Object: object}
	stripNoise(&live)
	gvk := groupVersionKind(arguments.APIVersion, resource.Kind)
	return airuntime.ToolResult{
		Text: catalogue.encode(live.Object),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
			Namespace: live.GetNamespace(), GVK: gvk, Name: live.GetName(),
			ResourceVersion: live.GetResourceVersion(),
		}},
		Target: &aisession.Target{
			Namespace: live.GetNamespace(), GVK: gvk, Name: live.GetName(),
		},
	}, nil
}

func (catalogue *Catalogue) describeResource(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments objectArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	identity, resource, err := catalogue.resolve(ctx, invocation.ClusterID, arguments.APIVersion, arguments.Kind)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	var result kubernetesdescribe.Result
	if resource.Kind == "Pod" && identity.Group == "" {
		result, err = catalogue.dependencies.Describe.DescribePod(ctx, kubernetesdescribe.PodInput{
			ClusterID: invocation.ClusterID, Namespace: arguments.Namespace, Name: arguments.Name,
		})
	} else {
		result, err = catalogue.dependencies.Describe.DescribeResource(ctx, kubernetesdescribe.ResourceInput{
			ClusterID: invocation.ClusterID, Resource: identity,
			Namespace: arguments.Namespace, Name: arguments.Name,
		})
	}
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	gvk := groupVersionKind(result.Target.APIVersion, result.Target.Kind)
	evidence := []aisession.Evidence{{
		Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
		Namespace: result.Target.Namespace, GVK: gvk, Name: result.Target.Name,
		ResourceVersion: result.Target.ResourceVersion,
	}}
	if len(result.Events.Items) > 0 {
		evidence = append(evidence, aisession.Evidence{
			Kind: aisession.EvidenceEvent, Cluster: invocation.ClusterID,
			Namespace: result.Target.Namespace, GVK: gvk, Name: result.Target.Name,
		})
	}
	return airuntime.ToolResult{
		Text:     catalogue.encode(describeDigest(result)),
		Evidence: evidence,
		Target: &aisession.Target{
			Namespace: result.Target.Namespace, GVK: gvk, Name: result.Target.Name,
		},
	}, nil
}

type listNodesArguments struct {
	LabelSelector string `json:"label_selector"`
	Limit         int    `json:"limit"`
}

func (catalogue *Catalogue) listNodes(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments listNodesArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	page, err := catalogue.dependencies.Resources.ListNodes(ctx, kubernetesresource.ListNodesInput{
		ClusterID:     invocation.ClusterID,
		Limit:         int64(bound(arguments.Limit, defaultListLimit, maxListLimit)),
		LabelSelector: arguments.LabelSelector,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	return airuntime.ToolResult{
		Text: fmt.Sprintf("返回 %d 个 Node：\n%s", len(page.Nodes), catalogue.encode(page.Nodes)),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID, GVK: "v1/Node",
		}},
	}, nil
}

type podLogsArguments struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	TailLines int    `json:"tail_lines"`
	Previous  bool   `json:"previous"`
}

func (catalogue *Catalogue) podLogs(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments podLogsArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	// The log stream is addressed by Pod identity, not by name: a name that was
	// recreated between the read and the answer would return another instance's
	// log under the old title. The model has no UID to pass, so the Pod is read
	// first — which is also where the container name comes from when a
	// single-container Pod was named without one.
	pod, err := catalogue.readPod(ctx, invocation.ClusterID, arguments.Namespace, arguments.Pod)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	container := strings.TrimSpace(arguments.Container)
	if container == "" {
		container, err = soleContainer(pod)
		if err != nil {
			return airuntime.ToolResult{}, err
		}
	}
	tail := int64(bound(arguments.TailLines, defaultLogLines, maxLogLines))
	// A bounded sink rather than the service byte ceiling: what reaches the
	// model has to fit the context it is going into, and the tail of a log is
	// the part that explains a failure.
	sink := &tailBuffer{limit: maxLogBytes}
	started := time.Now().UTC()
	_, err = catalogue.dependencies.Logs.Stream(ctx, podlogs.Input{
		ClusterID: invocation.ClusterID, Namespace: pod.GetNamespace(),
		PodName: pod.GetName(), PodUID: string(pod.GetUID()), Container: container,
		Previous: arguments.Previous, TailLines: &tail, Timestamps: true,
	}, sink)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	header := fmt.Sprintf("%s/%s 容器 %s 的最后 %d 行日志",
		pod.GetNamespace(), pod.GetName(), container, tail)
	if arguments.Previous {
		header += "（上一个已终止实例）"
	}
	return airuntime.ToolResult{
		Text: header + "：\n" + sink.String(),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceLog, Cluster: invocation.ClusterID,
			Namespace: pod.GetNamespace(), GVK: "v1/Pod", Name: pod.GetName(),
			Container: container, From: started, To: time.Now().UTC(),
		}},
		Target: &aisession.Target{
			Namespace: pod.GetNamespace(), GVK: "v1/Pod", Name: pod.GetName(),
		},
	}, nil
}

// readPod loads one Pod so a log read can be pinned to its identity.
func (catalogue *Catalogue) readPod(
	ctx context.Context, clusterID, namespace, name string,
) (*unstructured.Unstructured, error) {
	identity, _, err := catalogue.resolve(ctx, clusterID, "v1", "Pod")
	if err != nil {
		return nil, err
	}
	object, err := catalogue.dependencies.Resources.GetResource(ctx, kubernetesresource.GetResourceInput{
		ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return nil, err
	}
	pod := &unstructured.Unstructured{Object: object}
	if pod.GetUID() == "" {
		return nil, fmt.Errorf("%w: %s/%s 没有返回 Pod 身份", airuntime.ErrInvalidInput, namespace, name)
	}
	return pod, nil
}

// soleContainer names the only container a Pod has.
//
// Guessing for a multi-container Pod would answer a question about one
// container with the log of another, so the model is told to name one instead.
func soleContainer(pod *unstructured.Unstructured) (string, error) {
	names := make([]string, 0, 2)
	containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
	for _, entry := range containers {
		container, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := container["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%w: %s/%s 没有可读取日志的容器",
			airuntime.ErrInvalidInput, pod.GetNamespace(), pod.GetName())
	}
	return "", fmt.Errorf("%w: %s/%s 有多个容器（%s），请在 container 参数里指定一个",
		airuntime.ErrInvalidInput, pod.GetNamespace(), pod.GetName(), strings.Join(names, "、"))
}

// MetricsCatalogueLegend heads the query listing and explains its columns.
//
// The listing is a table rather than the indented JSON every other read tool
// returns, and the reason is the size cap those tools share: one object per
// query, with nine keys spelled out, is four times what a tool result may carry
// — and a pruned answer would silently drop the middle of the catalogue. Every
// other tool can be re-read against a narrower selector; this one cannot, so it
// has to fit whole. `metricsCatalogueFitsOneResult` holds it to that.
const MetricsCatalogueLegend = "指标查询目录。每行：查询名 | 标题 | 单位 | 标记。\n" +
	"标记：ns 可按 Namespace 收窄；ns! 必须给 Namespace；top 可给 Top N；top! 必须给 Top N；" +
	"instant 只返回当前值而不是曲线；ksm 需要集群已安装 kube-state-metrics；" +
	"node 需要已安装 node-exporter。没有标记表示都不需要。"

func (catalogue *Catalogue) listMetricQueries(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	_ = ctx
	_ = invocation
	return airuntime.ToolResult{
		Text: catalogue.prune(MetricsCatalogueListing(catalogue.dependencies.Metrics.Catalog())),
	}, nil
}

// MetricsCatalogueListing renders the catalogue as one line per query.
func MetricsCatalogueListing(definitions []metricsquery.Definition) string {
	lines := make([]string, 0, len(definitions)+1)
	lines = append(lines, MetricsCatalogueLegend)
	for _, definition := range definitions {
		line := fmt.Sprintf(
			"%s | %s | %s",
			definition.Name,
			definition.Title,
			definition.Unit,
		)
		if flags := metricQueryFlags(definition); flags != "" {
			line += " | " + flags
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// metricQueryFlags writes only what a caller has to act on.
//
// A query that supports nothing carries no flags at all: the common case is the
// one that should cost the fewest characters, and "supports_namespace": false
// repeated over a hundred entries is the difference between a listing that fits
// and one that gets cut in half.
func metricQueryFlags(definition metricsquery.Definition) string {
	flags := make([]string, 0, 4)
	switch {
	case definition.RequiresNamespace:
		flags = append(flags, "ns!")
	case definition.SupportsNamespace:
		flags = append(flags, "ns")
	}
	switch {
	case definition.RequiresTop:
		flags = append(flags, "top!")
	case definition.SupportsTop:
		flags = append(flags, "top")
	}
	if definition.Kind == metricsquery.KindInstant {
		flags = append(flags, "instant")
	}
	switch definition.RequiresComponent {
	case observability.ComponentKubeState:
		flags = append(flags, "ksm")
	case observability.ComponentNodeExporter:
		flags = append(flags, "node")
	}
	return strings.Join(flags, " ")
}

type queryMetricsArguments struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace"`
	Minutes   int    `json:"minutes"`
	Top       int    `json:"top"`
}

type queryCustomMetricsArguments struct {
	Expression string `json:"expression"`
	Kind       string `json:"kind"`
	Minutes    int    `json:"minutes"`
}

func (catalogue *Catalogue) queryMetrics(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments queryMetricsArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	minutes := bound(arguments.Minutes, 60, 1440)
	end := time.Now().UTC()
	start := end.Add(-time.Duration(minutes) * time.Minute)
	step := time.Duration(minutes) * time.Minute / 120
	if step < 30*time.Second {
		step = 30 * time.Second
	}
	result, err := catalogue.dependencies.Metrics.Query(ctx, metricsquery.Input{
		UserID: invocation.UserID, Name: arguments.Query, ClusterID: invocation.ClusterID,
		Namespace: arguments.Namespace, Start: start, End: end, Step: step, Top: arguments.Top,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	parameters, _ := json.Marshal(map[string]any{
		"namespace": arguments.Namespace, "minutes": minutes, "top": arguments.Top,
	})
	return airuntime.ToolResult{
		Text: catalogue.encode(metricsDigest(result, minutes)),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceMetric, Cluster: invocation.ClusterID,
			Namespace: arguments.Namespace, Query: arguments.Query,
			Parameters: string(parameters), From: start, To: end,
		}},
	}, nil
}

func (catalogue *Catalogue) queryCustomMetrics(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments queryCustomMetricsArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	kind := metricsquery.Kind(strings.TrimSpace(arguments.Kind))
	if kind == "" {
		kind = metricsquery.KindRange
	}
	if kind != metricsquery.KindRange && kind != metricsquery.KindInstant {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: kind 必须是 range 或 instant", airuntime.ErrInvalidInput,
		)
	}
	minutes := bound(arguments.Minutes, 60, 1440)
	end := time.Now().UTC()
	start := end.Add(-time.Duration(minutes) * time.Minute)
	step := time.Duration(minutes) * time.Minute / 120
	if step < 30*time.Second {
		step = 30 * time.Second
	}
	if kind == metricsquery.KindInstant {
		start = time.Time{}
		step = 0
	}
	result, err := catalogue.dependencies.Metrics.Explore(ctx, metricsquery.ExploreInput{
		UserID: invocation.UserID, ClusterID: invocation.ClusterID, Kind: kind,
		Start: start, End: end, Step: step,
		Queries: []metricsquery.ExploreQuery{{
			RefID: "A", Expression: arguments.Expression,
		}},
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if len(result.Queries) != 1 {
		return airuntime.ToolResult{}, errors.New("custom metrics query returned no outcome")
	}
	outcome := result.Queries[0]
	digest := customMetricsDigest(result, outcome, minutes)
	if outcome.Error != nil {
		return airuntime.ToolResult{Text: catalogue.encode(digest), Failed: true}, nil
	}
	parameters, _ := json.Marshal(map[string]any{
		"kind": kind, "minutes": minutes,
	})
	return airuntime.ToolResult{
		Text: catalogue.encode(digest),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceMetric, Cluster: invocation.ClusterID,
			Expression: arguments.Expression, Parameters: string(parameters),
			From: result.Start, To: result.End,
		}},
	}, nil
}

// --- Resolution -------------------------------------------------------------

// resolve turns the apiVersion and Kind a model wrote into the GVR the resource
// layer needs, using the target Cluster own discovery.
//
// Resolution is a read of the Cluster rather than a built-in table because a
// Cluster serves CRDs the Server has never heard of, and because a Kind that is
// not there should produce a clear answer the model can act on instead of a
// request the Agent refuses.
func (catalogue *Catalogue) resolve(
	ctx context.Context, clusterID, apiVersion, kind string,
) (kubernetesresource.ResourceIdentity, kubernetescatalog.Resource, error) {
	groupVersion, err := schema.ParseGroupVersion(strings.TrimSpace(apiVersion))
	if err != nil || strings.TrimSpace(kind) == "" {
		return kubernetesresource.ResourceIdentity{}, kubernetescatalog.Resource{},
			fmt.Errorf("%w: api_version 或 kind 无效", airuntime.ErrInvalidInput)
	}
	catalog, err := catalogue.discover(ctx, clusterID)
	if err != nil {
		return kubernetesresource.ResourceIdentity{}, kubernetescatalog.Resource{}, err
	}
	for _, resource := range catalog.Resources {
		if resource.Group == groupVersion.Group &&
			resource.Version == groupVersion.Version &&
			strings.EqualFold(resource.Kind, kind) {
			return kubernetesresource.ResourceIdentity{
				Group: resource.Group, Version: resource.Version, Resource: resource.Resource,
			}, resource, nil
		}
	}
	return kubernetesresource.ResourceIdentity{}, kubernetescatalog.Resource{}, fmt.Errorf(
		"%w: 目标 Cluster 不提供 %s 下的 %s，可先调用 list_api_resources 确认",
		airuntime.ErrInvalidInput, apiVersion, kind)
}

func (catalogue *Catalogue) discover(
	ctx context.Context, clusterID string,
) (kubernetescatalog.Catalog, error) {
	catalogue.mu.Lock()
	entry, ok := catalogue.catalog[clusterID]
	catalogue.mu.Unlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.value, nil
	}
	fresh, err := catalogue.dependencies.Resources.DiscoverResources(ctx, clusterID)
	if err != nil {
		return kubernetescatalog.Catalog{}, err
	}
	catalogue.mu.Lock()
	// Bounded so a Server asked about many Clusters does not keep a catalogue
	// for each of them forever. Discovery is cheap to redo and this is a burst
	// absorber, not a store.
	if len(catalogue.catalog) > 32 {
		catalogue.catalog = make(map[string]catalogEntry)
	}
	catalogue.catalog[clusterID] = catalogEntry{value: fresh, expiresAt: time.Now().Add(catalogTTL)}
	catalogue.mu.Unlock()
	return fresh, nil
}

// --- Shaping ----------------------------------------------------------------

// summarize reduces one object to what a diagnosis actually reads.
//
// Generic rather than per-Kind: the catalogue serves CRDs too, and a rule that
// only understands built-in Kinds would return nothing useful for exactly the
// objects an operator cannot inspect any other way. What it keeps is identity,
// age, the parts of `status` that report health, and the few `spec` fields that
// explain placement and scale.
func summarize(object unstructured.Unstructured) map[string]any {
	summary := map[string]any{"name": object.GetName()}
	if namespace := object.GetNamespace(); namespace != "" {
		summary["namespace"] = namespace
	}
	if created := object.GetCreationTimestamp().Time; !created.IsZero() {
		summary["age"] = shortDuration(time.Since(created))
	}
	if labels := object.GetLabels(); len(labels) > 0 {
		summary["labels"] = firstEntries(labels, 8)
	}
	status, _, _ := unstructured.NestedMap(object.Object, "status")
	for _, field := range []string{
		"phase", "replicas", "readyReplicas", "availableReplicas", "updatedReplicas",
		"unavailableReplicas", "currentReplicas", "desiredNumberScheduled",
		"numberReady", "succeeded", "failed", "active", "loadBalancer",
	} {
		if value, ok := status[field]; ok {
			summary[field] = value
		}
	}
	if conditions := abnormalConditions(status); len(conditions) > 0 {
		summary["conditions"] = conditions
	}
	if containers := containerStates(status); len(containers) > 0 {
		summary["containers"] = containers
	}
	spec, _, _ := unstructured.NestedMap(object.Object, "spec")
	for _, field := range []string{"nodeName", "replicas", "suspend", "type", "clusterIP", "schedule"} {
		if value, ok := spec[field]; ok {
			summary[field] = value
		}
	}
	return summary
}

// abnormalConditions keeps only the conditions that say something is wrong.
//
// A healthy object reports a dozen conditions that all say "True, everything is
// fine", and repeating them for every object in a listing is how a context
// window fills up with nothing.
func abnormalConditions(status map[string]any) []map[string]any {
	raw, ok := status["conditions"].([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		condition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		conditionStatus, _ := condition["status"].(string)
		healthy := conditionStatus == "True"
		// Conditions whose good state is False. Reporting them as problems
		// would invert the meaning of the most important line in the object.
		switch conditionType {
		case "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable", "Degraded":
			healthy = conditionStatus == "False"
		}
		if healthy {
			continue
		}
		entry := map[string]any{"type": conditionType, "status": conditionStatus}
		for _, field := range []string{"reason", "message"} {
			if value, _ := condition[field].(string); value != "" {
				entry[field] = value
			}
		}
		result = append(result, entry)
	}
	return result
}

func containerStates(status map[string]any) []map[string]any {
	result := make([]map[string]any, 0, 4)
	for _, key := range []string{"containerStatuses", "initContainerStatuses"} {
		raw, ok := status[key].([]any)
		if !ok {
			continue
		}
		for _, item := range raw {
			container, ok := item.(map[string]any)
			if !ok {
				continue
			}
			entry := map[string]any{"name": container["name"], "ready": container["ready"]}
			if restarts, ok := container["restartCount"]; ok {
				entry["restarts"] = restarts
			}
			if state, ok := container["state"].(map[string]any); ok {
				for phase, value := range state {
					detail, _ := value.(map[string]any)
					entry["state"] = phase
					if reason, _ := detail["reason"].(string); reason != "" {
						entry["reason"] = reason
					}
					if code, ok := detail["exitCode"]; ok {
						entry["exit_code"] = code
					}
				}
			}
			result = append(result, entry)
		}
	}
	return result
}

// describeDigest keeps the diagnostic half of a describe.
//
// The family projections are shaped for a Console that renders them; a model
// reading them whole would spend most of the answer on fields it never uses.
// What survives is the identity, what ZKE concluded, and the Events that
// support it.
func describeDigest(result kubernetesdescribe.Result) map[string]any {
	events := make([]map[string]any, 0, len(result.Events.Items))
	for _, event := range result.Events.Items {
		entry := map[string]any{
			"type": event.Type, "reason": event.Reason, "message": event.Message,
			"count": event.Count, "object": event.Regarding.Kind + "/" + event.Regarding.Name,
		}
		if event.LastSeen != nil {
			entry["last_seen"] = event.LastSeen.UTC().Format(time.RFC3339)
		}
		events = append(events, entry)
	}
	digest := map[string]any{
		"target": result.Target, "family": result.Family,
		"findings": result.Findings, "events": events,
		"events_truncated": result.Events.Truncated,
	}
	if result.Events.Omitted != "" {
		digest["events_omitted"] = result.Events.Omitted
	}
	if len(result.DegradedSections) > 0 {
		digest["degraded_sections"] = result.DegradedSections
	}
	if result.Pod != nil {
		digest["pod"] = result.Pod
	}
	if result.NodeResources != nil {
		digest["node_resources"] = result.NodeResources
	}
	if result.ServiceEndpoints != nil {
		digest["service_endpoints"] = result.ServiceEndpoints
	}
	if result.Related != nil {
		digest["related"] = result.Related
	}
	return digest
}

// metricsDigest turns a chart into numbers a model can reason about.
//
// A range query is hundreds of points per series. Passing them through would be
// unreadable and would push everything else out of the context; the shape of a
// series that matters to a diagnosis is its latest value, its peak and its
// average.
func metricsDigest(result metricsquery.Result, minutes int) map[string]any {
	digest := map[string]any{
		"query": result.Query, "title": result.Title, "unit": result.Unit,
		"window_minutes": minutes, "series": metricSeriesDigest(result.Series),
		"partial": result.Partial,
	}
	if len(result.Issues) > 0 {
		digest["issues"] = result.Issues
	}
	return digest
}

func customMetricsDigest(
	result metricsquery.ExploreResult,
	outcome metricsquery.ExploreOutcome,
	minutes int,
) map[string]any {
	digest := map[string]any{
		"expression": outcome.Expression, "effective_expression": outcome.EffectiveExpression,
		"kind": result.Kind, "series": metricSeriesDigest(outcome.Series),
		"truncated": outcome.Truncated,
	}
	if result.Kind == metricsquery.KindRange {
		digest["window_minutes"] = minutes
	}
	if outcome.Warning != "" {
		digest["warning"] = outcome.Warning
	}
	if outcome.Error != nil {
		digest["error"] = outcome.Error
	}
	if len(result.Issues) > 0 {
		digest["issues"] = result.Issues
	}
	return digest
}

func metricSeriesDigest(items []metricsquery.Series) []map[string]any {
	series := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var sum, peak float64
		var samples int
		var latest *float64
		for _, point := range item.Points {
			if point.Value == nil {
				continue
			}
			value := *point.Value
			sum += value
			if samples == 0 || value > peak {
				peak = value
			}
			samples++
			latest = point.Value
		}
		entry := map[string]any{"labels": item.Labels, "samples": samples}
		if samples > 0 {
			entry["latest"] = *latest
			entry["max"] = peak
			entry["avg"] = sum / float64(samples)
		}
		series = append(series, entry)
	}
	return series
}

// stripNoise removes the two fields that are large, machine-written, and never
// what somebody is reading an object to find out.
func stripNoise(object *unstructured.Unstructured) {
	annotations := object.GetAnnotations()
	if annotations == nil {
		return
	}
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	object.SetAnnotations(annotations)
	unstructured.RemoveNestedField(object.Object, "metadata", "managedFields")
}

// --- Helpers ----------------------------------------------------------------

// decode refuses anything the schema did not describe.
//
// DisallowUnknownFields is the enforcement half of `additionalProperties:
// false`: a model that sent `cluster` or `all_namespaces` is told its call was
// wrong instead of having the field dropped and getting an answer to a
// different question.
func decode(arguments json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %s", airuntime.ErrInvalidInput, "参数与工具 Schema 不匹配")
	}
	return nil
}

// encode renders one answer, bounded.
func (catalogue *Catalogue) encode(value any) string {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "无法序列化这次读取的结果。"
	}
	return catalogue.prune(string(payload))
}

// prune keeps the head and the tail of an oversized answer.
//
// Slicing is by Unicode code point, so a boundary cannot split a character in
// half; the marker between the halves is what tells the model the middle is
// missing rather than absent.
func (catalogue *Catalogue) prune(text string) string {
	runes := []rune(text)
	if len(runes) <= catalogue.config.ResultThresholdRunes {
		return text
	}
	head := string(runes[:catalogue.config.ResultHeadRunes])
	tail := string(runes[len(runes)-catalogue.config.ResultTailRunes:])
	return head + pruneMarker + tail
}

// tailBuffer keeps the last bytes written to it.
//
// The tail rather than the head: a container that crashed says why at the end
// of its log, and a head-bounded reader would return the startup banner every
// time.
type tailBuffer struct {
	limit  int
	buffer bytes.Buffer
}

func (sink *tailBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	sink.buffer.Write(payload)
	if sink.buffer.Len() > sink.limit*2 {
		sink.trim()
	}
	return written, nil
}

func (sink *tailBuffer) trim() {
	if sink.buffer.Len() <= sink.limit {
		return
	}
	// Copied before the reset: the slice aliases the buffer own storage, and
	// writing it back into the buffer it came from would overwrite it midway.
	kept := append([]byte(nil), sink.buffer.Bytes()[sink.buffer.Len()-sink.limit:]...)
	// Start at a line boundary. Half a log line at the top of an excerpt reads
	// as a corrupted message rather than as a shortened one.
	if index := bytes.IndexByte(kept, '\n'); index >= 0 && index+1 < len(kept) {
		kept = kept[index+1:]
	}
	sink.buffer.Reset()
	sink.buffer.Write(kept)
}

func (sink *tailBuffer) String() string {
	sink.trim()
	if sink.buffer.Len() == 0 {
		return "(容器没有输出日志)"
	}
	return sink.buffer.String()
}

func bound(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func groupVersionKind(apiVersion, kind string) string {
	return strings.TrimSpace(apiVersion) + "/" + strings.TrimSpace(kind)
}

func firstEntries(values map[string]string, limit int) map[string]string {
	if len(values) <= limit {
		return values
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]string, limit)
	for _, key := range keys[:limit] {
		result[key] = values[key]
	}
	return result
}

func shortDuration(value time.Duration) string {
	switch {
	case value < time.Hour:
		return fmt.Sprintf("%dm", int(value.Minutes()))
	case value < 48*time.Hour:
		return fmt.Sprintf("%dh", int(value.Hours()))
	default:
		return fmt.Sprintf("%dd", int(value.Hours()/24))
	}
}
