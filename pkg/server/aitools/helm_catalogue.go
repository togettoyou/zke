package aitools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/helm"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

// The chart catalogue, for the model.
//
// These four exist because of what the install and upgrade tools need and
// cannot invent: `repository_id` is a UUID an administrator assigned, and there
// is nothing in a Cluster that names it. Without a way to enumerate the
// catalogue, the write tools are a door with no handle — the model can only
// guess an identifier it has never been told, and every guess comes back as a
// refusal that reads like a broken Cluster.
//
// Nothing here reaches a Cluster. The chart catalogue is one platform-wide list
// of what may be installed anywhere, which is also why its permission is not a
// Cluster permission: `helm.repository.read` has a global scope floor, so it is
// resolved at global scope here rather than declared in the tool spec — the
// runtime checks a spec's permissions against the session's Cluster, and a
// permission checked one scope too narrow is a permission checked wrong.
//
// What comes back is deliberately less than the Console's own view. A
// repository row carries the username it authenticates with, its CA certificate
// and its public keyring; none of that helps a model choose a chart, and the
// half of a credential is not something to put in a model context to save it a
// question. Identity, description and whether the repository is enabled are
// what choosing needs.

// GlobalPermissionResolver answers a permission question that has no Cluster.
//
// Separate from ClusterPermissionResolver because the two are not
// interchangeable: `helm.repository.read` cannot be exercised by a Project
// binding, so asking for it at Project scope would grant it to a binding the
// Console's own routes refuse.
type GlobalPermissionResolver interface {
	AuthorizeGlobal(context.Context, string, rbac.Permission) error
}

// HelmChartReader is the curated chart catalogue, read-only.
type HelmChartReader interface {
	ListRepositories(context.Context) (helm.RepositoryPage, error)
	ListCharts(context.Context, string, string, int) (helm.ChartPage, error)
	ListChartVersions(context.Context, string, string) (helm.ChartVersionPage, error)
	GetChart(context.Context, string, string, string) (helm.ChartDetail, error)
}

// How many charts one listing returns. A repository publishes thousands; a
// model choosing one narrows by name rather than reading the index.
const (
	defaultChartListLimit = 30
	maxChartListLimit     = 100
)

type chartListArguments struct {
	RepositoryID string `json:"repository_id"`
	Search       string `json:"search"`
	Limit        int    `json:"limit"`
}

type chartArguments struct {
	RepositoryID string `json:"repository_id"`
	Chart        string `json:"chart"`
	Version      string `json:"version"`
}

type chartVersionsArguments struct {
	RepositoryID string `json:"repository_id"`
	Chart        string `json:"chart"`
}

func (catalogue *Catalogue) listHelmRepositories(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	if result, denied := catalogue.authorizeChartCatalogue(ctx, invocation); denied {
		return result, nil
	}
	page, err := catalogue.dependencies.Charts.ListRepositories(ctx)
	if err != nil {
		if result, handled := helmCatalogueFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	rows := make([]map[string]any, 0, len(page.Repositories))
	for _, repository := range page.Repositories {
		rows = append(rows, map[string]any{
			"repository_id": repository.ID,
			"name":          repository.Name,
			"description":   repository.Description,
			// A disabled repository is still listed, because "there is no such
			// repository" and "an administrator switched it off" are different
			// things to tell an operator.
			"enabled": repository.Enabled,
		})
	}
	return airuntime.ToolResult{
		Text: fmt.Sprintf(
			"平台维护的 Chart 仓库共 %d 个（repository_id 是安装与升级工具唯一接受的仓库标识）：\n%s",
			len(rows), catalogue.encode(rows)),
	}, nil
}

func (catalogue *Catalogue) listHelmCharts(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments chartListArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if result, denied := catalogue.authorizeChartCatalogue(ctx, invocation); denied {
		return result, nil
	}
	page, err := catalogue.dependencies.Charts.ListCharts(
		ctx, arguments.RepositoryID, arguments.Search,
		bound(arguments.Limit, defaultChartListLimit, maxChartListLimit),
	)
	if err != nil {
		if result, handled := helmCatalogueFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	rows := make([]map[string]any, 0, len(page.Charts))
	for _, chart := range page.Charts {
		rows = append(rows, map[string]any{
			"chart": chart.Name, "latest_version": chart.Version,
			"app_version": chart.AppVersion, "description": chart.Description,
			"deprecated": chart.Deprecated, "version_count": chart.VersionCount,
		})
	}
	header := fmt.Sprintf("返回 %d 个 Chart（该仓库共 %d 个）", len(rows), page.Total)
	// Stale is not a detail. "This chart is missing" and "this list is from
	// Tuesday" look identical to a reader who was not told which one it is.
	if page.Stale {
		header += "；仓库当前读不到，这份列表来自本地缓存（" +
			page.FetchedAt.UTC().Format(time.RFC3339) + "）"
	}
	return airuntime.ToolResult{Text: header + "：\n" + catalogue.encode(rows)}, nil
}

func (catalogue *Catalogue) listHelmChartVersions(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments chartVersionsArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if result, denied := catalogue.authorizeChartCatalogue(ctx, invocation); denied {
		return result, nil
	}
	page, err := catalogue.dependencies.Charts.ListChartVersions(
		ctx, arguments.RepositoryID, arguments.Chart)
	if err != nil {
		if result, handled := helmCatalogueFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	rows := make([]map[string]any, 0, len(page.Versions))
	for _, version := range page.Versions {
		row := map[string]any{
			"version": version.Version, "app_version": version.AppVersion,
			"deprecated": version.Deprecated,
		}
		if !version.Created.IsZero() {
			row["created"] = version.Created.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return airuntime.ToolResult{
		Text: fmt.Sprintf("Chart %s 发布了 %d 个版本：\n%s",
			arguments.Chart, len(rows), catalogue.encode(rows)),
	}, nil
}

func (catalogue *Catalogue) getHelmChart(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments chartArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if result, denied := catalogue.authorizeChartCatalogue(ctx, invocation); denied {
		return result, nil
	}
	detail, err := catalogue.dependencies.Charts.GetChart(
		ctx, arguments.RepositoryID, arguments.Chart, arguments.Version)
	if err != nil {
		if result, handled := helmCatalogueFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	digest := map[string]any{
		"chart": detail.Name, "version": detail.Version,
		"app_version": detail.AppVersion, "description": detail.Description,
		"deprecated": detail.Deprecated, "type": detail.Type,
		"home": detail.Home, "sources": detail.Sources,
		// The chart's own defaults, verbatim and with their comments: it is the
		// only place the valid value paths are written down, and writing an
		// override without reading it is how a values document fails the
		// chart's schema. It is chart content, not cluster content — nothing
		// here came out of a Release.
		"values": detail.Values,
	}
	if detail.ValuesSchema != "" {
		digest["values_schema_present"] = true
	}
	return airuntime.ToolResult{
		Text: fmt.Sprintf("Chart %s %s：\n%s",
			detail.Name, detail.Version, catalogue.encode(digest)),
	}, nil
}

// authorizeChartCatalogue asks the one question these tools raise, at the scope
// the permission actually has.
func (catalogue *Catalogue) authorizeChartCatalogue(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, bool) {
	err := catalogue.dependencies.GlobalPermissions.AuthorizeGlobal(
		ctx, invocation.UserID, rbac.PermissionHelmRepositoryRead)
	if err == nil {
		return airuntime.ToolResult{}, false
	}
	if !errors.Is(err, rbac.ErrDenied) {
		// Anything else is a failure to decide, and a failure to decide is not
		// permission. Reported as a denial rather than raised, so the model is
		// told the catalogue is unreadable instead of retrying it as a
		// transient Cluster error.
		return airuntime.ToolResult{
			Text: "无法判定当前账户对 Chart 仓库目录的权限，未读取。", Failed: true, Denied: true,
		}, true
	}
	return airuntime.ToolResult{
		Text: fmt.Sprintf(
			"当前账户没有全局 %s 权限，读不到 Chart 仓库目录。"+
				"没有它就无法取得 repository_id，因此也无法安装或升级 Release。",
			rbac.PermissionHelmRepositoryRead),
		Failed: true, Denied: true,
	}, true
}

// helmCatalogueFailure separates the answers that are about the catalogue from
// the ones that are about this Server.
//
// The first of these is what an end-to-end test found: a `repository_id` the
// model guessed came back through the runtime's generic text as "the Agent may
// be unreachable or the object may not exist", which is wrong twice — no Agent
// is involved, and the fix is to list the repositories rather than to retry.
func helmCatalogueFailure(err error) (airuntime.ToolResult, bool) {
	result := airuntime.ToolResult{Failed: true}
	switch {
	case errors.Is(err, store.ErrHelmRepositoryNotFound),
		errors.Is(err, helm.ErrInvalidInput):
		result.Text = "没有这个 Chart 仓库。repository_id 是平台目录里的标识，不能凭名称猜测；" +
			"请先调用 list_helm_repositories。"
	case errors.Is(err, helm.ErrRepositoryDisabled):
		result.Text = "该 Chart 仓库已被平台管理员停用，读不到它的 Chart。"
	case errors.Is(err, helm.ErrChartNotFound):
		result.Text = "该仓库里没有这个 Chart 或这个版本；请先用 list_helm_charts 确认名称。"
	case errors.Is(err, helm.ErrRepositoryUnreachable):
		result.Text = "Chart 仓库暂时读不到，本地也没有可用的索引缓存。这不是集群问题。"
	case errors.Is(err, helm.ErrChartOCIUnsupported):
		result.Text = "这个 Chart 只发布在 OCI registry 上，ZKE 不从这里读取它的内容。"
	case errors.Is(err, helm.ErrChartTooLarge):
		result.Text = "Chart 归档超过可读取的大小上限。"
	case errors.Is(err, helm.ErrChartUnsigned), errors.Is(err, helm.ErrChartSignatureInvalid):
		result.Text = "该仓库要求 Chart 带可校验的来源证明，这个版本没有通过校验，因此不可读取也不可安装。"
	default:
		return airuntime.ToolResult{}, false
	}
	return result, true
}

// chartCatalogueSpecs is the catalogue half of Helm.
//
// Assembled here rather than in build() because these four share one
// permission story that takes a paragraph to state, and stating it beside the
// tools is what keeps the next person from moving `helm.repository.read` into
// the spec where the runtime would check it at the wrong scope.
func (catalogue *Catalogue) chartCatalogueSpecs() []airuntime.ToolSpec {
	if catalogue.dependencies.Charts == nil ||
		catalogue.dependencies.GlobalPermissions == nil {
		return nil
	}
	repositoryProperty := stringProperty(
		"list_helm_repositories 返回的仓库标识，不是仓库名称。")
	chartProperty := stringProperty("Chart 名称。")
	// `ai.run` is what the spec declares because it is what these tools use:
	// they read platform configuration and touch no Cluster. The permission
	// that actually gates them is resolved inside each call — see
	// authorizeChartCatalogue — at the global scope it has to be checked at.
	permissions := []rbac.Permission{rbac.PermissionAIRun}
	conditional := []rbac.Permission{rbac.PermissionHelmRepositoryRead}
	return []airuntime.ToolSpec{
		{
			Name: toolListHelmRepositories,
			Description: "列出平台维护的 Chart 仓库目录，返回每个仓库的 repository_id、名称和是否启用。" +
				"安装或升级 Release 前必须先调用它取得 repository_id——那是一个平台分配的标识，集群里没有任何地方能推断出它。",
			Schema:                 objectSchema(nil, nil),
			Permissions:            permissions,
			ConditionalPermissions: conditional,
		},
		{
			Name: toolListHelmCharts,
			Description: "列出某个仓库中可安装的 Chart：名称、最新版本、appVersion 与简介。" +
				"用 search 按名称或关键字缩小范围，不要翻整个索引。",
			Schema: objectSchema(map[string]any{
				"repository_id": repositoryProperty,
				"search":        stringProperty("按 Chart 名称、简介或关键字做子串过滤。"),
				"limit":         integerProperty("返回上限，默认 30，最大 100。"),
			}, []string{"repository_id"}),
			Permissions:            permissions,
			ConditionalPermissions: conditional,
		},
		{
			Name: toolListHelmChartVersions,
			Description: "列出一个 Chart 已发布的版本、appVersion 与发布时间。" +
				"需要固定版本或降级到旧版本时使用；只要最新版本时可以省略 version。",
			Schema: objectSchema(map[string]any{
				"repository_id": repositoryProperty, "chart": chartProperty,
			}, []string{"repository_id", "chart"}),
			Permissions:            permissions,
			ConditionalPermissions: conditional,
		},
		{
			Name: toolGetHelmChart,
			Description: "读取一个 Chart 版本的元信息和它自带的 values.yaml 默认值。" +
				"要为安装或升级撰写 values 之前先读它：合法的 values 路径只写在这里，" +
				"凭空撰写的 values 会被 Chart 自己的 values.schema.json 拒绝。",
			Schema: objectSchema(map[string]any{
				"repository_id": repositoryProperty, "chart": chartProperty,
				"version": stringProperty("Chart 版本；留空表示该仓库发布的最新版本。"),
			}, []string{"repository_id", "chart"}),
			Permissions:            permissions,
			ConditionalPermissions: conditional,
		},
	}
}
