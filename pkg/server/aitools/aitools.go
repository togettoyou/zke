// Package aitools is the read-only tool catalogue AIOps offers to the model.
//
// It exists as its own package so the runtime can stay free of Kubernetes: the
// runtime owns authorization, budgets, approvals and the trail, and this owns
// what a read actually does and how large its answer is allowed to be. Every
// tool here goes through a service ZKE already has, which means it goes through
// the target Cluster Agent and inherits that path bounds; none of them holds a
// credential of its own and none of them can address a Cluster other than the
// one the runtime passes in.
//
// Two properties matter more than the individual tools. The first is that
// output is summarized rather than dumped: a model asked to read a Cluster does
// not need every field of every object, and an unbounded answer would spend the
// context window on YAML nobody reads. The second is that every answer that
// makes a claim also carries evidence, so a conclusion drawn from it leads back
// to a view an operator can open and check.
package aitools

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

// What one tool answer may occupy in the model context, in Unicode code points.
//
// A result above the threshold keeps its head and its tail with a marker
// between them rather than being cut at the front. Which half matters depends
// entirely on the tool: the head of a listing says what was returned and in what
// shape, while a container that crashed says why at the end of its log. Cutting
// only the front is how a model ends up reasoning about a startup banner.
//
// The bound is announced in the text rather than applied silently: a model that
// knows the answer was trimmed asks a narrower question, and one that does not
// draws a conclusion from a partial answer it believes is complete.
const (
	DefaultResultThresholdRunes = 8192
	DefaultResultHeadRunes      = 4096
	DefaultResultTailRunes      = 1024
)

// pruneMarker stands in for every removed middle.
const pruneMarker = "\n…（中间部分已省略；如需完整内容请缩小 namespace、选择器或 limit 后重新读取）…\n"

// Config is what a deployment may set about tool answers.
//
// A zero value is a working configuration: every budget falls back to the
// shipped default above.
type Config struct {
	ResultThresholdRunes int
	ResultHeadRunes      int
	ResultTailRunes      int
}

func (config Config) normalized() Config {
	if config.ResultThresholdRunes <= 0 {
		config.ResultThresholdRunes = DefaultResultThresholdRunes
	}
	if config.ResultHeadRunes <= 0 {
		config.ResultHeadRunes = DefaultResultHeadRunes
	}
	if config.ResultTailRunes <= 0 {
		config.ResultTailRunes = DefaultResultTailRunes
	}
	// The emitted answer has to be smaller than the threshold, or pruning would
	// grow a result that was already inside the budget.
	for config.ResultHeadRunes+config.ResultTailRunes+
		len([]rune(pruneMarker)) >= config.ResultThresholdRunes {
		if config.ResultTailRunes > 1 {
			config.ResultTailRunes /= 2
			continue
		}
		config.ResultHeadRunes = config.ResultThresholdRunes / 2
		break
	}
	return config
}

// ErrUnknownTool is a name that is not in the catalogue. The runtime already
// refuses those; this is the second answer for a catalogue that changed between
// the advertisement and the call.
var ErrUnknownTool = errors.New("unknown AIOps tool")

const (
	// defaultListLimit and maxListLimit bound one listing. A model that wants
	// more should narrow the selector rather than page through a Cluster.
	defaultListLimit = 50
	maxListLimit     = 200
	defaultLogLines  = 200
	maxLogLines      = 2000
	maxLogBytes      = 24 * 1024
	// catalogTTL is how long a Cluster API catalogue answers for. Discovery is
	// a full round trip to the Agent and the answer changes when a CRD is
	// installed, not between two steps of one turn.
	catalogTTL = 60 * time.Second
)

type ResourceReader interface {
	DiscoverResources(context.Context, string) (kubernetescatalog.Catalog, error)
	ListResources(context.Context, kubernetesresource.ListResourcesInput) (kubernetesresource.ResourcePage, error)
	GetResource(context.Context, kubernetesresource.GetResourceInput) (map[string]any, error)
	ListNodes(context.Context, kubernetesresource.ListNodesInput) (kubernetesresource.NodePage, error)
}

type OverviewReader interface {
	Get(context.Context, string) (clusteroverview.Overview, error)
}

type DescribeReader interface {
	DescribePod(context.Context, kubernetesdescribe.PodInput) (kubernetesdescribe.Result, error)
	DescribeResource(context.Context, kubernetesdescribe.ResourceInput) (kubernetesdescribe.Result, error)
}

type LogReader interface {
	Stream(context.Context, podlogs.Input, io.Writer) (podlogs.Result, error)
}

type MetricsReader interface {
	Catalog() []metricsquery.Definition
	Query(context.Context, metricsquery.Input) (metricsquery.Result, error)
}

type Dependencies struct {
	Resources ResourceReader
	Overview  OverviewReader
	Describe  DescribeReader
	Logs      LogReader
	Metrics   MetricsReader
}

// Catalogue is the ToolSet the runtime advertises.
//
// A dependency the deployment did not compose simply removes its tools from
// the catalogue rather than leaving one that fails at call time: a model plans
// against the list it is given, and a tool that is advertised and then always
// fails wastes a step every time.
type Catalogue struct {
	dependencies Dependencies
	config       Config
	specs        []airuntime.ToolSpec

	mu      sync.Mutex
	catalog map[string]catalogEntry
}

type catalogEntry struct {
	value     kubernetescatalog.Catalog
	expiresAt time.Time
}

func New(dependencies Dependencies, config Config) *Catalogue {
	catalogue := &Catalogue{
		dependencies: dependencies, config: config.normalized(),
		catalog: make(map[string]catalogEntry),
	}
	catalogue.specs = catalogue.build()
	return catalogue
}

func (catalogue *Catalogue) Specs() []airuntime.ToolSpec { return catalogue.specs }

func (catalogue *Catalogue) Invoke(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	switch invocation.Name {
	case toolClusterOverview:
		return catalogue.clusterOverview(ctx, invocation)
	case toolListAPIResources:
		return catalogue.listAPIResources(ctx, invocation)
	case toolListResources:
		return catalogue.listResources(ctx, invocation)
	case toolGetResource:
		return catalogue.getResource(ctx, invocation)
	case toolDescribeResource:
		return catalogue.describeResource(ctx, invocation)
	case toolListNodes:
		return catalogue.listNodes(ctx, invocation)
	case toolPodLogs:
		return catalogue.podLogs(ctx, invocation)
	case toolListMetricQueries:
		return catalogue.listMetricQueries(ctx, invocation)
	case toolQueryMetrics:
		return catalogue.queryMetrics(ctx, invocation)
	default:
		return airuntime.ToolResult{}, ErrUnknownTool
	}
}

const (
	toolClusterOverview   = "cluster_overview"
	toolListAPIResources  = "list_api_resources"
	toolListResources     = "list_resources"
	toolGetResource       = "get_resource"
	toolDescribeResource  = "describe_resource"
	toolListNodes         = "list_nodes"
	toolPodLogs           = "get_pod_logs"
	toolListMetricQueries = "list_metric_queries"
	toolQueryMetrics      = "query_metrics"
)

func (catalogue *Catalogue) build() []airuntime.ToolSpec {
	specs := make([]airuntime.ToolSpec, 0, 9)
	if catalogue.dependencies.Overview != nil {
		specs = append(specs, airuntime.ToolSpec{
			Name: toolClusterOverview,
			Description: "读取目标 Cluster 的整体快照：Node、Namespace、Pod、工作负载与存储的数量与状态分布。" +
				"排查的第一步通常从这里开始，用来判断问题是集群级还是局部的。",
			Schema:      objectSchema(nil, nil),
			Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		})
	}
	if catalogue.dependencies.Resources != nil {
		specs = append(specs,
			airuntime.ToolSpec{
				Name: toolListAPIResources,
				Description: "列出目标 Cluster 支持的 API 资源类型（含 CRD），用于确认某个 Kind 是否存在、" +
					"属于哪个 apiVersion、是否是 Namespace 级。不确定 Kind 时先调用它。",
				Schema: objectSchema(map[string]any{
					"search": stringProperty("按 Kind、资源名或分组做子串过滤，例如 deployment、networking.k8s.io。"),
				}, nil),
				Permissions: []rbac.Permission{rbac.PermissionClusterRead},
			},
			airuntime.ToolSpec{
				Name: toolListResources,
				Description: "列出某个 Kind 的对象，返回名称、创建时间与状态摘要。" +
					"支持 Namespace、标签选择器与字段选择器，用于缩小排查范围。不返回完整对象。",
				Schema: objectSchema(map[string]any{
					"api_version":    stringProperty("对象的 apiVersion，例如 v1、apps/v1、networking.k8s.io/v1。"),
					"kind":           stringProperty("对象的 Kind，例如 Pod、Deployment、Ingress。"),
					"namespace":      stringProperty("Namespace。Namespace 级资源留空表示全集群。"),
					"label_selector": stringProperty("标签选择器，例如 app=web,tier!=cache。"),
					"field_selector": stringProperty("字段选择器，例如 status.phase=Running。"),
					"limit":          integerProperty("返回上限，默认 50，最大 200。"),
				}, []string{"api_version", "kind"}),
				Permissions: []rbac.Permission{rbac.PermissionClusterRead},
			},
			airuntime.ToolSpec{
				Name: toolGetResource,
				Description: "读取单个对象的完整定义（已去除 managedFields 与 last-applied 注解）。" +
					"需要看 spec 细节、镜像、探针、资源请求或 status 具体字段时使用。",
				Schema: objectSchema(map[string]any{
					"api_version": stringProperty("对象的 apiVersion。"),
					"kind":        stringProperty("对象的 Kind。"),
					"namespace":   stringProperty("Namespace，集群级对象留空。"),
					"name":        stringProperty("对象名称。"),
				}, []string{"api_version", "kind", "name"}),
				Permissions: []rbac.Permission{rbac.PermissionClusterRead},
			},
			airuntime.ToolSpec{
				Name: toolListNodes,
				Description: "列出 Node 及其状态、可调度性、容量与 kubelet 版本。" +
					"怀疑调度、资源不足或节点异常时使用。",
				Schema: objectSchema(map[string]any{
					"label_selector": stringProperty("标签选择器。"),
					"limit":          integerProperty("返回上限，默认 50，最大 200。"),
				}, nil),
				Permissions: []rbac.Permission{rbac.PermissionClusterRead},
			},
		)
	}
	if catalogue.dependencies.Describe != nil {
		specs = append(specs, airuntime.ToolSpec{
			Name: toolDescribeResource,
			Description: "诊断单个对象：返回它的关键状态、ZKE 归纳出的问题点，以及指向它的 Kubernetes Event。" +
				"定位“为什么这个对象不正常”时，优先用它而不是 get_resource。",
			Schema: objectSchema(map[string]any{
				"api_version": stringProperty("对象的 apiVersion。"),
				"kind":        stringProperty("对象的 Kind。"),
				"namespace":   stringProperty("Namespace，集群级对象留空。"),
				"name":        stringProperty("对象名称。"),
			}, []string{"api_version", "kind", "name"}),
			Permissions: []rbac.Permission{
				rbac.PermissionClusterRead, rbac.PermissionClusterEventRead,
			},
		})
	}
	if catalogue.dependencies.Logs != nil {
		specs = append(specs, airuntime.ToolSpec{
			Name: toolPodLogs,
			Description: "读取某个 Pod 容器的日志尾部。日志可能包含敏感内容，属于 ZKE 的敏感能力，" +
				"在需要用户批准的模式下会先停下来等待批准。",
			Schema: objectSchema(map[string]any{
				"namespace":  stringProperty("Pod 所在 Namespace。"),
				"pod":        stringProperty("Pod 名称。"),
				"container":  stringProperty("容器名。单容器 Pod 可以省略；多容器 Pod 必须指定。"),
				"tail_lines": integerProperty("读取的尾部行数，默认 200，最大 2000。"),
				"previous":   booleanProperty("读取上一个已终止实例的日志，用于排查 CrashLoopBackOff。"),
			}, []string{"namespace", "pod"}),
			Permissions: []rbac.Permission{rbac.PermissionClusterPodLogsRead},
			Sensitive:   true,
		})
	}
	if catalogue.dependencies.Metrics != nil {
		specs = append(specs,
			airuntime.ToolSpec{
				Name: toolListMetricQueries,
				Description: "列出可用的指标查询目录。ZKE 不接受任意 PromQL，只能调用目录里的查询，" +
					"因此先用它确认查询名与它支持的参数。",
				Schema:      objectSchema(nil, nil),
				Permissions: []rbac.Permission{rbac.PermissionClusterMetricsRead},
			},
			airuntime.ToolSpec{
				Name: toolQueryMetrics,
				Description: "执行目录中的一个指标查询，返回每条曲线的最新值、最大值与平均值摘要。" +
					"用于确认资源用量、饱和度与趋势。",
				Schema: objectSchema(map[string]any{
					"query":     stringProperty("list_metric_queries 返回的查询名。"),
					"namespace": stringProperty("Namespace，仅当该查询支持时可用。"),
					"minutes":   integerProperty("回看窗口分钟数，默认 60，最大 1440。"),
					"top":       integerProperty("Top N，仅当该查询支持或要求时可用。"),
				}, []string{"query"}),
				Permissions: []rbac.Permission{rbac.PermissionClusterMetricsRead},
			},
		)
	}
	return specs
}
