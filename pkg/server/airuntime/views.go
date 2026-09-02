package airuntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// toolOpenConsoleView opens one ZKE application on the operator's desktop.
//
// Everything else in the catalogue answers a question; this one moves the
// desk. It exists because some answers are not text: "内存最近一小时涨到多少"
// is answered by a chart, and an agent that can write the expression but not
// show it leaves the last step — open 监控, pick the Cluster, paste, press
// 执行查询 — to the person who asked precisely so they would not have to.
//
// It is deliberately the *escalation* rather than the default. A conclusion
// already carries evidence, and an evidence chip is an invitation the operator
// takes when they want it; taking over their screen is a stronger act, so it
// has to be chosen, said out loud in `reason`, and it happens at most once per
// turn. What it can point at is exactly what an evidence reference can point
// at, which is what keeps one permission table and one set of receiving
// screens instead of a second addressing scheme beside them.
//
// It grants nothing. The application that comes forward re-authorizes every
// request as the operator, and the Console refuses to act on an intent while
// they are not looking at AIOps — so the worst a mistaken call can do is put a
// window in front of somebody, which they can close.
const toolOpenConsoleView = "open_console_view"

// openConsoleViewSpec advertises the one tool whose effect is on a screen.
func openConsoleViewSpec() ToolSpec {
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"show": map[string]any{
				"type": "string",
				"description": "要展示的东西，决定打开哪个 ZKE 应用：" +
					"metric 打开监控（具名查询落到对应图表分区，自定义表达式落到数据探索）；" +
					"resource、event、log 打开容器服务并定位到该对象；helm_release 打开容器服务的 Helm 分区。",
				"enum": []string{"metric", "resource", "event", "log", "helm_release"},
			},
			"reason": map[string]any{
				"type": "string",
				"description": "一句话说明为什么现在替操作者打开它。" +
					"这是他对一次自己没有发起的动作的唯一说明，会原样显示在对话里。",
				"maxLength": 200,
			},
			"query": describedString(
				"show=metric 时的具名查询名，取自 list_metric_queries。与 expression 二选一。"),
			"expression": describedString(
				"show=metric 时的 MetricsQL 表达式，与 query 二选一。" +
					"Server 会把会话 Cluster 强制注入选择器；不要在表达式里组装 Cluster ID。"),
			"run": map[string]any{
				"type": "boolean",
				"description": "show=metric 时是否在打开后立即执行查询，默认 true。" +
					"只有在表达式仍需操作者自己改写时才设为 false。",
			},
			"api_version": describedString(
				"show=resource、event 或 log 时对象的 apiVersion，例如 v1、apps/v1。"),
			"kind": describedString(
				"show=resource、event 或 log 时对象的 Kind，例如 Pod、Deployment。"),
			"namespace": describedString(
				"对象所在 Namespace。集群级对象留空；log 与 helm_release 必填。"),
			"name": describedString(
				"对象或 Helm Release 名称。留空表示打开该 Kind 的列表。"),
			"container": describedString("show=log 时的容器名，单容器 Pod 可以省略。"),
		},
		"required":             []string{"show", "reason"},
		"additionalProperties": false,
	})
	if err != nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return ToolSpec{
		Name: toolOpenConsoleView,
		Description: "在操作者当前的 ZKE 桌面上打开一个应用并定位到指定视图：指标打开监控并可直接执行查询，" +
			"对象、Event 与日志打开容器服务。用于“这件事看图或看对象比看文字快”的时候，" +
			"或者用户本来就是让你打开它。每轮最多一次；操作者当时没在看 AIOps 时，Console 会改为在对话里留下入口。" +
			"它只切换操作者看到的画面，不读取集群内容，也不改变任何权限。",
		Schema: schema,
		// The tool itself reads nothing: `ai.run` is its own boundary, exactly
		// as it is for load_skill. What the operator must still hold is the
		// permission for the view being opened, which depends on what is being
		// shown — so it is resolved and rechecked below, and advertised here as
		// conditional rather than demanded all at once.
		Permissions: []rbac.Permission{rbac.PermissionAIRun},
		ConditionalPermissions: []rbac.Permission{
			rbac.PermissionClusterRead,
			rbac.PermissionClusterEventRead,
			rbac.PermissionClusterMetricsRead,
			rbac.PermissionClusterPodLogsRead,
			rbac.PermissionClusterSecretRead,
		},
	}
}

// describedString keeps the schema above readable. The catalogue package has
// its own builders; the runtime advertises two tools and does not need them.
func describedString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

type openConsoleViewRequest struct {
	Show       string `json:"show"`
	Reason     string `json:"reason"`
	Query      string `json:"query"`
	Expression string `json:"expression"`
	Run        *bool  `json:"run"`
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Container  string `json:"container"`
}

// openConsoleView turns one request into a checked view intent.
//
// The result text is written here rather than assembled from the request: it
// is the one tool answer besides a skill that no Cluster contributed to, and
// keeping the model's own strings out of it is what lets it be marked trusted
// without opening a way to write instructions into its own context.
func (runtime *Runtime) openConsoleView(
	ctx context.Context, job turnJob, arguments json.RawMessage,
) (ToolResult, error) {
	var request openConsoleViewRequest
	if err := decodeStrict(arguments, &request); err != nil {
		return ToolResult{}, err
	}
	target, err := viewTarget(request)
	if err != nil {
		return ToolResult{}, err
	}
	if target.Kind == aisession.EvidenceMetric && target.Query != "" {
		validator, available := runtime.tools.(MetricViewValidator)
		if !available || !validator.HasMetricQuery(target.Query) {
			return ToolResult{}, fmt.Errorf(
				"%w: 指标查询 %q 不在当前目录中，请先用 list_metric_queries 搜索确认",
				ErrInvalidInput, target.Query,
			)
		}
	}
	target.Cluster = job.clusterID
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		return ToolResult{}, fmt.Errorf("%w: reason 不能为空", ErrInvalidInput)
	}
	// One per turn. A run that opens a window, then another, then a third has
	// stopped being an answer and started being remote control of somebody
	// else's screen — and the Console enforces the same bound, so a model told
	// it opened three views would be describing a desktop that only moved once.
	if job.opened != nil && !job.opened.CompareAndSwap(false, true) {
		return ToolResult{
			Text: "本轮已经替操作者打开过一个视图，Console 不会再打开第二个。" +
				"其余引用请作为结论里的证据给出，由操作者自己点开。",
			Failed: true, Trusted: true,
		}, nil
	}
	if err := runtime.authorizeEvidence(
		ctx, job.userID, job.tenantID, job.projectID, job.clusterID,
		[]aisession.Evidence{target},
	); err != nil {
		return ToolResult{
			Text: "操作者没有查看该视图所需的权限，Console 不会打开它。" +
				"请改用其他已有权限的依据说明结论。",
			Failed: true, Denied: true, Trusted: true,
		}, nil
	}
	// Only an operator-authored expression has anything to run: a catalogue
	// query lands on a panel that loads itself, and recording `run` there would
	// be the trail claiming something the receiving screen never did.
	runnable := target.Kind == aisession.EvidenceMetric && target.Expression != ""
	run := runnable
	if request.Run != nil {
		run = *request.Run && runnable
	}
	return ToolResult{
		Text: "已请求 Console 在操作者的桌面上打开这个视图。" +
			"接下来直接说明这张视图上能看到什么、要看哪里，不要再让操作者自己去打开一次；" +
			"操作者当时没有在看 AIOps 时，Console 会改为在对话里留下入口。",
		Trusted: true,
		View:    &aisession.View{Target: target, Run: run, Reason: reason},
	}, nil
}

// viewTarget checks that the request names something a view can actually open.
//
// A metric view without a query is an empty chart and a log view without a Pod
// is a Namespace listing — both are windows that opened to say nothing, which
// is worse for the operator than not opening at all. The check is here rather
// than left to the Console: a target the receiving screen cannot use should be
// a corrected tool call, not a silent no-op on a desktop.
func viewTarget(request openConsoleViewRequest) (aisession.Evidence, error) {
	target := aisession.Evidence{
		Namespace: strings.TrimSpace(request.Namespace),
		Name:      strings.TrimSpace(request.Name),
		Container: strings.TrimSpace(request.Container),
	}
	kind := strings.TrimSpace(request.Show)
	gvk := groupVersionKind(request.APIVersion, request.Kind)
	switch aisession.EvidenceKind(kind) {
	case aisession.EvidenceMetric:
		target.Kind = aisession.EvidenceMetric
		target.Query = strings.TrimSpace(request.Query)
		target.Expression = strings.TrimSpace(request.Expression)
		if (target.Query == "") == (target.Expression == "") {
			return target, fmt.Errorf(
				"%w: show=metric 时 query 与 expression 必须且只能给出一个", ErrInvalidInput,
			)
		}
	case aisession.EvidenceResource, aisession.EvidenceEvent:
		target.Kind = aisession.EvidenceKind(kind)
		if gvk == "" {
			return target, fmt.Errorf(
				"%w: show=%s 时需要 api_version 与 kind", ErrInvalidInput, kind,
			)
		}
		target.GVK = gvk
	case aisession.EvidenceLog:
		target.Kind = aisession.EvidenceLog
		if target.Namespace == "" || target.Name == "" {
			return target, fmt.Errorf("%w: show=log 时需要 namespace 与 name", ErrInvalidInput)
		}
		if gvk == "" {
			gvk = "v1/Pod"
		}
		target.GVK = gvk
	case aisession.EvidenceHelmRelease:
		target.Kind = aisession.EvidenceHelmRelease
		if target.Namespace == "" || target.Name == "" {
			return target, fmt.Errorf(
				"%w: show=helm_release 时需要 namespace 与 name", ErrInvalidInput,
			)
		}
	default:
		return target, fmt.Errorf("%w: show 不是可以打开的视图类型", ErrInvalidInput)
	}
	return target, nil
}

// groupVersionKind is the reference form the Console reads, and the same one
// the catalogue writes onto its own evidence.
func groupVersionKind(apiVersion, kind string) string {
	apiVersion, kind = strings.TrimSpace(apiVersion), strings.TrimSpace(kind)
	if apiVersion == "" || kind == "" {
		return ""
	}
	return apiVersion + "/" + kind
}

// visibleView drops an intent the operator may no longer act on.
//
// Same rule as visibleEvidence, and for the same reason: a window that opens on
// a view the Server would refuse is a claim of access in the record. The check
// is repeated here because the tool ran before it — permissions are rechecked
// per call, not per turn, and this is the last point before the intent becomes
// durable.
func (runtime *Runtime) visibleView(
	ctx context.Context, job turnJob, view *aisession.View,
) *aisession.View {
	if view == nil {
		return nil
	}
	view.Target.Cluster = job.clusterID
	if runtime.authorizeEvidence(ctx, job.userID, job.tenantID, job.projectID, job.clusterID,
		[]aisession.Evidence{view.Target}) != nil {
		return nil
	}
	return view
}
