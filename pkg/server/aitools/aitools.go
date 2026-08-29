// Package aitools is the bounded tool catalogue AIOps offers to the model.
//
// It exists as its own package so the runtime can stay free of Kubernetes: the
// runtime owns authorization, budgets, approvals and the trail, and this owns
// what a tool actually does and how large its answer is allowed to be. Every
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
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
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
	DefaultTerminalRevalidate   = 15 * time.Second
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
	ManifestPreviewTTL   time.Duration
	MaxManifestBytes     int
	MaxManifestDocuments int
	TerminalRevalidate   time.Duration
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
	if config.ManifestPreviewTTL <= 0 {
		config.ManifestPreviewTTL = 15 * time.Minute
	}
	if config.MaxManifestBytes <= 0 {
		config.MaxManifestBytes = 256 * 1024
	}
	if config.MaxManifestDocuments <= 0 {
		config.MaxManifestDocuments = 64
	}
	if config.TerminalRevalidate <= 0 {
		config.TerminalRevalidate = DefaultTerminalRevalidate
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

// WorkloadWriter is the existing Kubernetes workload mutation path narrowed to
// the first AIOps write: scaling a Deployment or StatefulSet. The service
// implementation still enforces DryRun, confirmation and Agent idempotency;
// the catalogue does not open a second write path beside it.
type WorkloadWriter interface {
	ScaleWorkload(
		context.Context,
		kubernetesresource.ScaleWorkloadInput,
	) (kubernetesresource.WorkloadDetail, error)
}

type WorkloadRevisionWriter interface {
	ListWorkloadRevisions(context.Context, kubernetesresource.ListWorkloadRevisionsInput) (kubernetesresource.WorkloadRevisionPage, error)
	RollbackWorkload(context.Context, kubernetesresource.RollbackWorkloadInput) (kubernetesresource.WorkloadDetail, error)
}

// HelmReleaseReader is Helm's own storage, read-only.
//
// It is separate from ResourceReader because a release is not a Kubernetes
// kind and answers to a different permission: it lives in a Secret, so reading
// one requires `cluster.secret.read` as well as `cluster.read`. See
// helm_reads.go for what is returned and, more importantly, what is not.
type HelmReleaseReader interface {
	ListHelmReleases(
		context.Context,
		kubernetesresource.ListHelmReleasesInput,
	) (kubernetesresource.HelmReleasePage, error)
	ListHelmReleaseRevisions(
		context.Context, string, string, string,
	) (kubernetesresource.HelmReleasePage, error)
	GetHelmRelease(
		context.Context, string, string, string, int64,
	) (kubernetesresource.HelmReleaseDetail, error)
}

type ClusterScopeResolver interface {
	ResolveClusterScope(context.Context, string) (rbac.ResolvedScope, error)
	AuthorizeResolvedCluster(context.Context, string, rbac.Permission, rbac.ResolvedScope) error
}

type ClusterPermissionResolver interface {
	AuthorizeCluster(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error)
}

type TerminalCommander interface {
	CreateCommandSession(context.Context, clusterterminal.CommandSessionInput) (clusterterminal.CommandSession, error)
	ExecuteCommand(context.Context, clusterterminal.CommandInput) (clusterterminal.CommandResult, error)
	FinishCommandSession(context.Context, clusterterminal.CommandSession) error
}

type ManifestWriter interface {
	Execute(context.Context, kubernetesmanifest.ResourceAccess, kubernetesmanifest.Input) (kubernetesmanifest.Result, error)
}

type ManifestAccessFactory func(kubernetesresource.ManifestGrant) kubernetesmanifest.ResourceAccess

type Dependencies struct {
	Resources         ResourceReader
	Overview          OverviewReader
	Describe          DescribeReader
	Logs              LogReader
	Metrics           MetricsReader
	Helm              HelmReleaseReader
	HelmWrites        HelmReleaseWriter
	Charts            HelmChartReader
	Workloads         WorkloadWriter
	Revisions         WorkloadRevisionWriter
	Scopes            ClusterScopeResolver
	Manifests         ManifestWriter
	ManifestAccess    ManifestAccessFactory
	Terminal          TerminalCommander
	Permissions       ClusterPermissionResolver
	GlobalPermissions GlobalPermissionResolver
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

	mu          sync.Mutex
	catalog     map[string]catalogEntry
	previews    map[string]*writePreview
	rollbacks   map[string]*rollbackPreview
	helmChanges map[string]*helmPreview
	terminals   map[string]*terminalTurn
}

type catalogEntry struct {
	value     kubernetescatalog.Catalog
	expiresAt time.Time
}

func New(dependencies Dependencies, config Config) *Catalogue {
	catalogue := &Catalogue{
		dependencies: dependencies, config: config.normalized(),
		catalog:     make(map[string]catalogEntry),
		previews:    make(map[string]*writePreview),
		rollbacks:   make(map[string]*rollbackPreview),
		helmChanges: make(map[string]*helmPreview),
		terminals:   make(map[string]*terminalTurn),
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
	case toolPreviewWorkloadScale:
		return catalogue.scaleWorkload(ctx, invocation, true)
	case toolScaleWorkload:
		return catalogue.scaleWorkload(ctx, invocation, false)
	case toolPreviewManifestApply:
		return catalogue.previewManifest(ctx, invocation, kubernetesmanifest.OperationApply)
	case toolApplyManifest:
		return catalogue.executeManifest(ctx, invocation, kubernetesmanifest.OperationApply)
	case toolPreviewManifestDelete:
		return catalogue.previewManifest(ctx, invocation, kubernetesmanifest.OperationDelete)
	case toolDeleteManifest:
		return catalogue.executeManifest(ctx, invocation, kubernetesmanifest.OperationDelete)
	case toolListWorkloadRevisions:
		return catalogue.listWorkloadRevisions(ctx, invocation)
	case toolPreviewWorkloadRollback:
		return catalogue.previewWorkloadRollback(ctx, invocation)
	case toolRollbackWorkload:
		return catalogue.rollbackWorkload(ctx, invocation)
	case toolListHelmReleases:
		return catalogue.listHelmReleases(ctx, invocation)
	case toolListHelmReleaseRevisions:
		return catalogue.listHelmReleaseRevisions(ctx, invocation)
	case toolGetHelmRelease:
		return catalogue.getHelmRelease(ctx, invocation)
	case toolListHelmRepositories:
		return catalogue.listHelmRepositories(ctx, invocation)
	case toolListHelmCharts:
		return catalogue.listHelmCharts(ctx, invocation)
	case toolListHelmChartVersions:
		return catalogue.listHelmChartVersions(ctx, invocation)
	case toolGetHelmChart:
		return catalogue.getHelmChart(ctx, invocation)
	case toolPreviewHelmInstall:
		return catalogue.previewHelmInstall(ctx, invocation)
	case toolPreviewHelmUpgrade:
		return catalogue.previewHelmUpgrade(ctx, invocation)
	case toolPreviewHelmRollback:
		return catalogue.previewHelmRollback(ctx, invocation)
	case toolPreviewHelmUninstall:
		return catalogue.previewHelmUninstall(ctx, invocation)
	case toolApplyHelmChange:
		return catalogue.applyHelmChange(ctx, invocation)
	case toolRunTerminalCommand:
		return catalogue.runTerminalCommand(ctx, invocation)
	default:
		return airuntime.ToolResult{}, ErrUnknownTool
	}
}

const (
	toolClusterOverview          = "cluster_overview"
	toolListAPIResources         = "list_api_resources"
	toolListResources            = "list_resources"
	toolGetResource              = "get_resource"
	toolDescribeResource         = "describe_resource"
	toolListNodes                = "list_nodes"
	toolPodLogs                  = "get_pod_logs"
	toolListMetricQueries        = "list_metric_queries"
	toolQueryMetrics             = "query_metrics"
	toolPreviewWorkloadScale     = "preview_workload_scale"
	toolScaleWorkload            = "scale_workload"
	toolPreviewManifestApply     = "preview_manifest_apply"
	toolApplyManifest            = "apply_manifest"
	toolPreviewManifestDelete    = "preview_manifest_delete"
	toolDeleteManifest           = "delete_manifest"
	toolListWorkloadRevisions    = "list_workload_revisions"
	toolPreviewWorkloadRollback  = "preview_workload_rollback"
	toolRollbackWorkload         = "rollback_workload"
	toolRunTerminalCommand       = "run_terminal_command"
	toolListHelmReleases         = "list_helm_releases"
	toolListHelmReleaseRevisions = "list_helm_release_revisions"
	toolGetHelmRelease           = "get_helm_release"
	toolPreviewHelmInstall       = "preview_helm_install"
	toolPreviewHelmUpgrade       = "preview_helm_upgrade"
	toolPreviewHelmRollback      = "preview_helm_rollback"
	toolPreviewHelmUninstall     = "preview_helm_uninstall"
	toolApplyHelmChange          = "apply_helm_release_change"
	toolListHelmRepositories     = "list_helm_repositories"
	toolListHelmCharts           = "list_helm_charts"
	toolListHelmChartVersions    = "list_helm_chart_versions"
	toolGetHelmChart             = "get_helm_chart"
)

func (catalogue *Catalogue) build() []airuntime.ToolSpec {
	specs := make([]airuntime.ToolSpec, 0, 31)
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
	if catalogue.dependencies.Workloads != nil && catalogue.dependencies.Scopes != nil {
		scaleSchema := objectSchema(map[string]any{
			"kind": enumStringProperty(
				"只允许 Deployment 或 StatefulSet。", "Deployment", "StatefulSet",
			),
			"namespace": stringProperty("工作负载所在 Namespace。受保护 Namespace 会改用对应的独立管理权限。"),
			"name":      stringProperty("工作负载名称。"),
			"replicas":  nonNegativeIntegerProperty("目标副本数，必须大于等于 0。"),
		}, []string{"kind", "namespace", "name", "replicas"})
		workloadWritePermissions := []rbac.Permission{
			rbac.PermissionClusterResourceUpdate,
			rbac.PermissionClusterSystemNamespaceManage,
			rbac.PermissionClusterAgentNamespaceManage,
		}
		specs = append(specs,
			airuntime.ToolSpec{
				Name: toolPreviewWorkloadScale,
				Description: "对目标 Deployment 或 StatefulSet 的副本数变更执行 Kubernetes 服务端 DryRun。" +
					"它不会改变集群；实际伸缩前先调用此工具，并把返回的目标与副本数展示给操作者。",
				Schema:                 scaleSchema,
				Target:                 workloadScaleTarget,
				ConditionalPermissions: workloadWritePermissions,
			},
			airuntime.ToolSpec{
				Name: toolScaleWorkload,
				Description: "实际调整目标 Deployment 或 StatefulSet 的副本数。" +
					"调用前应先用 preview_workload_scale 对完全相同的参数完成 DryRun。",
				Schema:                 scaleSchema,
				Target:                 workloadScaleTarget,
				ConditionalPermissions: workloadWritePermissions,
				Mutating:               true,
				Sensitive:              true,
			},
		)
	}
	if catalogue.dependencies.Manifests != nil &&
		catalogue.dependencies.ManifestAccess != nil &&
		catalogue.dependencies.Scopes != nil {
		manifestSchema := objectSchema(map[string]any{
			"manifest":  stringProperty("严格多文档 Kubernetes YAML；不得包含 Secret。"),
			"namespace": stringProperty("为未声明 metadata.namespace 的 Namespace 级对象提供默认值。"),
			"force":     booleanProperty("Apply 时是否强制接管字段所有权；仅在冲突且操作者明确要求时使用。"),
		}, []string{"manifest"})
		deletePreviewSchema := objectSchema(map[string]any{
			"manifest":  stringProperty("只用 apiVersion、kind、metadata.name/namespace 标识待删除对象；不得包含 Secret。"),
			"namespace": stringProperty("为未声明 metadata.namespace 的 Namespace 级对象提供默认值。"),
		}, []string{"manifest"})
		executeSchema := objectSchema(map[string]any{
			"preview_id": stringProperty("对应预检返回的服务端快照 ID；不可提交任意新 YAML。"),
		}, []string{"preview_id"})
		manifestPermissions := []rbac.Permission{
			rbac.PermissionClusterResourceCreate,
			rbac.PermissionClusterResourceUpdate,
			rbac.PermissionClusterResourceDelete,
			rbac.PermissionClusterNamespaceManage,
			rbac.PermissionClusterNodeManage,
			rbac.PermissionClusterSecretRead,
			rbac.PermissionClusterSecretManage,
			rbac.PermissionClusterRBACManage,
			rbac.PermissionClusterSystemNamespaceManage,
			rbac.PermissionClusterAgentNamespaceManage,
		}
		specs = append(specs,
			airuntime.ToolSpec{
				Name:                   toolPreviewManifestApply,
				Description:            "严格解析多文档 YAML，逐对象判权并执行 Kubernetes 服务端 DryRun，返回动作和有界差异；不改变集群。",
				Schema:                 manifestSchema,
				Permissions:            []rbac.Permission{rbac.PermissionClusterRead},
				ConditionalPermissions: manifestPermissions,
			},
			airuntime.ToolSpec{
				Name:        toolApplyManifest,
				Description: "提交已预检的 Manifest Apply；只接受 preview_id，批准后重验权限并再次 DryRun。",
				Schema:      executeSchema, Target: catalogue.previewTarget,
				Permissions: []rbac.Permission{rbac.PermissionClusterRead}, Mutating: true,
				ConditionalPermissions: manifestPermissions,
				SensitiveWhen:          catalogue.previewSensitive,
			},
			airuntime.ToolSpec{
				Name:                   toolPreviewManifestDelete,
				Description:            "逐对象判权并 DryRun 预检 Manifest 删除；返回将删除或已不存在的对象，不改变集群。",
				Schema:                 deletePreviewSchema,
				Permissions:            []rbac.Permission{rbac.PermissionClusterRead},
				ConditionalPermissions: manifestPermissions,
			},
			airuntime.ToolSpec{
				Name:        toolDeleteManifest,
				Description: "提交已预检的 Manifest 删除；只接受 preview_id，批准后重验权限并再次 DryRun。",
				Schema:      executeSchema, Target: catalogue.previewTarget,
				Permissions: []rbac.Permission{rbac.PermissionClusterRead}, Mutating: true, Sensitive: true,
				ConditionalPermissions: manifestPermissions,
			},
		)
	}
	if catalogue.dependencies.Revisions != nil && catalogue.dependencies.Resources != nil &&
		catalogue.dependencies.Scopes != nil {
		targetSchema := map[string]any{
			"kind":      enumStringProperty("支持 Deployment、StatefulSet 或 DaemonSet。", "Deployment", "StatefulSet", "DaemonSet"),
			"namespace": stringProperty("工作负载所在 Namespace。"),
			"name":      stringProperty("工作负载名称。"),
		}
		rollbackSchema := map[string]any{}
		for key, value := range targetSchema {
			rollbackSchema[key] = value
		}
		rollbackSchema["revision"] = positiveIntegerProperty("目标历史版本号。")
		rollbackSchema["uid"] = stringProperty("读取历史时工作负载的 UID。")
		rollbackSchema["resource_version"] = stringProperty("读取历史时工作负载的 resourceVersion；变更后会以冲突拒绝。")
		executeSchema := objectSchema(map[string]any{
			"preview_id": stringProperty("回滚 DryRun 返回的服务端快照 ID。"),
		}, []string{"preview_id"})
		workloadWritePermissions := []rbac.Permission{
			rbac.PermissionClusterResourceUpdate,
			rbac.PermissionClusterSystemNamespaceManage,
			rbac.PermissionClusterAgentNamespaceManage,
		}
		specs = append(specs,
			airuntime.ToolSpec{
				Name:        toolListWorkloadRevisions,
				Description: "读取 Deployment、StatefulSet 或 DaemonSet 的历史 Pod 模板版本；回滚前先用它选择 current=false 的 revision，没有非当前版本时不能回滚。",
				Schema:      objectSchema(targetSchema, []string{"kind", "namespace", "name"}),
				Target:      workloadRevisionTarget, Permissions: []rbac.Permission{rbac.PermissionClusterRead},
			},
			airuntime.ToolSpec{
				Name:        toolPreviewWorkloadRollback,
				Description: "对指定 current=false 的历史版本执行 Kubernetes 服务端 DryRun；不改变集群，并保存提交所需的精确快照。",
				Schema:      objectSchema(rollbackSchema, []string{"kind", "namespace", "name", "revision", "uid", "resource_version"}),
				Target:      workloadRevisionTarget, Permissions: []rbac.Permission{rbac.PermissionClusterRead},
				ConditionalPermissions: workloadWritePermissions,
			},
			airuntime.ToolSpec{
				Name:        toolRollbackWorkload,
				Description: "提交已预检的工作负载回滚；批准后重新判权并再次 DryRun。",
				Schema:      executeSchema, Target: catalogue.previewTarget,
				Permissions: []rbac.Permission{rbac.PermissionClusterRead}, Mutating: true,
				ConditionalPermissions: workloadWritePermissions,
				SensitiveWhen:          catalogue.previewSensitive,
			},
		)
	}
	if catalogue.dependencies.Helm != nil {
		// Both permissions on every entry, and both rechecked before every call.
		// `cluster.read` because this addresses a Cluster like any other read,
		// `cluster.secret.read` because a Helm release *is* a Secret — the same
		// pair the Console's release routes require. Neither stands in for the
		// other.
		helmPermissions := []rbac.Permission{
			rbac.PermissionClusterRead, rbac.PermissionClusterSecretRead,
		}
		namespaceProperty := stringProperty("Release 安装到的 Namespace。")
		nameProperty := stringProperty("Release 名称，不是它渲染出的工作负载名称。")
		listSchema := map[string]any{"namespace": namespaceProperty}
		releaseSchema := map[string]any{
			"namespace": namespaceProperty, "name": nameProperty,
		}
		revisionSchema := map[string]any{
			"namespace": namespaceProperty, "name": nameProperty,
			"revision": positiveIntegerProperty(
				"要读取的历史版本号；省略表示存储中保留的最新版本。"),
		}
		specs = append(specs,
			airuntime.ToolSpec{
				Name: toolListHelmReleases,
				Description: "列出某个 Namespace 中安装的 Helm Release：名称、当前 revision、状态与最后写入时间。" +
					"想知道某个工作负载属于哪个应用、或者它是不是被 Helm 管理时使用；不返回 values。",
				Schema:      objectSchema(listSchema, []string{"namespace"}),
				Permissions: helmPermissions,
			},
			airuntime.ToolSpec{
				Name: toolListHelmReleaseRevisions,
				Description: "读取一个 Release 的修订历史：每个 revision 的版本号、状态、写入时间，并标出当前版本。" +
					"判断“最近是不是升级过”“上一个可用版本是哪个”时使用；不返回 values。",
				Schema:      objectSchema(releaseSchema, []string{"namespace", "name"}),
				Target:      helmReleaseTarget,
				Permissions: helmPermissions,
			},
			airuntime.ToolSpec{
				Name: toolGetHelmRelease,
				Description: "读取一个 Release 某个 revision 的详情：Chart 名称与版本、appVersion、状态说明、部署时间、" +
					"被覆盖的 values 路径，以及该 Chart 渲染出的对象清单。" +
					"values 取值、NOTES.txt 与 Manifest 正文属于 Secret 内容，不会返回。",
				Schema:      objectSchema(revisionSchema, []string{"namespace", "name"}),
				Target:      helmReleaseTarget,
				Permissions: helmPermissions,
				Sensitive:   true,
			},
		)
	}
	specs = append(specs, catalogue.chartCatalogueSpecs()...)
	if catalogue.dependencies.HelmWrites != nil && catalogue.dependencies.Scopes != nil {
		// The three every release change needs whatever it does, rechecked by
		// the runtime before every call. The rest depend on the action and the
		// target Namespace and are resolved inside the tool — see
		// helm_writes.go — so they are advertised as candidates rather than as
		// a set the operator must hold all of.
		helmWritePermissions := []rbac.Permission{
			rbac.PermissionClusterRead,
			rbac.PermissionClusterHelmManage,
			rbac.PermissionClusterSecretManage,
		}
		helmConditionalPermissions := []rbac.Permission{
			rbac.PermissionClusterResourceCreate,
			rbac.PermissionClusterResourceUpdate,
			rbac.PermissionClusterResourceDelete,
			rbac.PermissionClusterSystemNamespaceManage,
			rbac.PermissionClusterAgentNamespaceManage,
			rbac.PermissionClusterManage,
		}
		namespaceProperty := stringProperty("Release 所在的 Namespace。")
		nameProperty := stringProperty("Release 名称。")
		waitProperty := booleanProperty(
			"是否等待对象就绪；等待会占用本轮的时间预算，默认不等待。")
		timeoutProperty := positiveIntegerProperty(
			"等待秒数，仅在 wait=true 时有效；默认 300，最大 600。")
		valuesProperty := stringProperty(
			"values YAML 文档，最多 3 KiB；留空表示使用 Chart 自带默认值。" +
				"绝不能包含密码、Token、证书或其他凭证明文——它会进入 AIOps 轨迹并发送到模型端点；" +
				"需要凭证或更长配置的 Chart 请让用户在 ZKE 的 Helm 应用中完成。")
		repositoryProperty := stringProperty(
			"Chart 仓库 ID，来自平台维护的仓库目录；不接受任意地址。")
		chartProperty := stringProperty("Chart 名称。")
		versionProperty := stringProperty("Chart 版本；留空表示该仓库发布的最新版本。")
		specs = append(specs,
			airuntime.ToolSpec{
				Name: toolPreviewHelmInstall,
				Description: "对一次 Helm 安装执行 Helm 自己的 DryRun：渲染 Chart、逐对象校验，返回将要创建的对象清单和 preview_id。" +
					"不改变集群。Chart 只能来自平台维护的仓库目录。",
				Schema: objectSchema(map[string]any{
					"namespace":        namespaceProperty,
					"name":             nameProperty,
					"repository_id":    repositoryProperty,
					"chart":            chartProperty,
					"version":          versionProperty,
					"values":           valuesProperty,
					"create_namespace": booleanProperty("Namespace 不存在时是否创建。"),
					"wait":             waitProperty,
					"timeout_seconds":  timeoutProperty,
				}, []string{"namespace", "name", "repository_id", "chart"}),
				Target:                 helmInstallTarget,
				Permissions:            helmWritePermissions,
				ConditionalPermissions: helmConditionalPermissions,
			},
			airuntime.ToolSpec{
				Name: toolPreviewHelmUpgrade,
				Description: "对一次 Helm 升级执行 DryRun，返回将要创建或替换的对象清单和 preview_id；不改变集群。" +
					"只想换 Chart 版本、保留现有配置时用 reuse_values=true，不要重新提交 values。",
				Schema: objectSchema(map[string]any{
					"namespace":       namespaceProperty,
					"name":            nameProperty,
					"repository_id":   repositoryProperty,
					"chart":           chartProperty,
					"version":         versionProperty,
					"values":          valuesProperty,
					"reuse_values":    booleanProperty("沿用上一个 revision 的 values；与 values 互斥。"),
					"wait":            waitProperty,
					"timeout_seconds": timeoutProperty,
				}, []string{"namespace", "name", "repository_id", "chart"}),
				Target:                 helmUpgradeTarget,
				Permissions:            helmWritePermissions,
				ConditionalPermissions: helmConditionalPermissions,
			},
			airuntime.ToolSpec{
				Name: toolPreviewHelmRollback,
				Description: "对一次 Helm 回滚执行 DryRun，返回目标 revision 将要恢复的对象清单和 preview_id；不改变集群。" +
					"先用 list_helm_release_revisions 选择一个 current=false 的 revision。",
				Schema: objectSchema(map[string]any{
					"namespace":       namespaceProperty,
					"name":            nameProperty,
					"revision":        positiveIntegerProperty("要回到的历史版本号。"),
					"wait":            waitProperty,
					"timeout_seconds": timeoutProperty,
				}, []string{"namespace", "name", "revision"}),
				Target:                 helmRollbackTarget,
				Permissions:            helmWritePermissions,
				ConditionalPermissions: helmConditionalPermissions,
			},
			airuntime.ToolSpec{
				Name: toolPreviewHelmUninstall,
				Description: "对一次 Helm 卸载执行 DryRun，返回将要删除的对象清单和 preview_id；不改变集群。" +
					"keep_history=true 会保留修订历史，这是之后还想回滚的前提。",
				Schema: objectSchema(map[string]any{
					"namespace":       namespaceProperty,
					"name":            nameProperty,
					"keep_history":    booleanProperty("删除对象后保留 Release 的修订历史。"),
					"wait":            waitProperty,
					"timeout_seconds": timeoutProperty,
				}, []string{"namespace", "name"}),
				Target:                 helmUninstallTarget,
				Permissions:            helmWritePermissions,
				ConditionalPermissions: helmConditionalPermissions,
			},
			airuntime.ToolSpec{
				Name: toolApplyHelmChange,
				Description: "提交一次已预检的 Helm Release 变更（安装、升级、回滚或卸载）。只接受 preview_id，" +
					"不能提交新的 Chart、values 或 revision；批准后重新判权并再次 DryRun。" +
					"一次变更会写入该应用拥有的每一个对象，因此始终按敏感操作处理。",
				Schema: objectSchema(map[string]any{
					"preview_id": stringProperty("对应预检返回的服务端快照 ID。"),
				}, []string{"preview_id"}),
				Target:                 catalogue.helmChangeTarget,
				Permissions:            helmWritePermissions,
				ConditionalPermissions: helmConditionalPermissions,
				Mutating:               true,
				Sensitive:              true,
			},
		)
	}
	if catalogue.dependencies.Terminal != nil && catalogue.dependencies.Permissions != nil {
		specs = append(specs, airuntime.ToolSpec{
			Name: toolRunTerminalCommand,
			Description: "在目标 Cluster 的本轮隔离终端中执行一条非交互 Shell 命令；同一 AIOps Turn 的后续命令复用该终端，Turn 结束自动清理。" +
				"可使用 kubectl、BusyBox、curl 和 jq；kubectl 只能执行当前用户权限投射后允许的操作，" +
				"kubectl exec 还必须持有 cluster.pod.exec，受保护 Namespace 再叠加对应管理权限。命令与有界输出会进入 AIOps 轨迹和模型上下文，" +
				"不得在命令中放入 Secret、Token、密码或其他凭证明文。优先用于取证；可能变更状态的命令必须自身幂等，" +
				"响应不确定时不得盲目重试。AIOps 不投射 Secret 读写权限。",
			Schema: objectSchema(map[string]any{
				"command": stringProperty("要交给 /bin/sh -c 的单条命令；不得包含任何凭证明文。"),
			}, []string{"command"}),
			Permissions: []rbac.Permission{rbac.PermissionClusterTerminalExec},
			Sensitive:   true,
			Mutating:    true,
		})
	}
	return specs
}
