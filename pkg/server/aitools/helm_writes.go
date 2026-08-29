package aitools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/helm"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

// Changing a Helm release from AIOps.
//
// This is the longest permission stack in the catalogue, and it is long for the
// same reason the Console's release routes are: one request renders a chart and
// writes every object an application owns. Nothing about that is covered by a
// single-object permission, so each part is asked separately, and every one of
// them but the last has to answer yes:
//
//   - `cluster.read`, because this addresses a Cluster like every other tool;
//   - `cluster.helm.manage`, because changing a release is its own capability;
//   - `cluster.secret.manage`, because Helm's release storage *is* a Secret;
//   - the object permissions the action actually spends — create and update for
//     an install, an upgrade or a rollback, delete for an uninstall. Holding
//     the Helm permission does not conjure the power to write objects;
//   - the protected-Namespace grant when the release lives in the Agent's own
//     Namespace or a `kube-` one;
//   - `cluster.manage`, which is not required but decides whether a chart may
//     render objects no Namespace contains. It is resolved from the operator,
//     never taken from a tool argument, and the Agent refuses the rendered
//     manifest by name when the answer was no.
//
// The first three are static and are rechecked by the runtime before every
// call. The rest depend on the action and the target Namespace, so the tools
// resolve them here — at preview time, so a refusal arrives before anybody is
// asked to approve anything, and again at execution time, because a permission
// can be withdrawn while an approval is waiting.
//
// The shape follows the Manifest tools rather than inventing a second one: a
// preview runs Helm's own dry run through the target Cluster's Agent and stores
// a server-side snapshot bound to this operator and this Cluster; the executing
// tool accepts only a `preview_id` and cannot be handed a different change. One
// executing tool serves all four actions because the snapshot already says
// which one it is — and all four stop for a person, because there is no such
// thing as a routine Helm write.
//
// What a report carries back is bounded the same way the read tools bound a
// release: the rendered manifest and NOTES.txt are Secret content and are
// reduced to an inventory of object identities. Which objects a change touches
// is exactly what an approver needs; the bodies are in the Helm application,
// under the approver's own session.

type helmAction string

const (
	helmActionInstall   helmAction = "install"
	helmActionUpgrade   helmAction = "upgrade"
	helmActionRollback  helmAction = "rollback"
	helmActionUninstall helmAction = "uninstall"
)

const (
	// What a model-authored values document may be.
	//
	// Helm's own transfer bound is a megabyte, and this is far below it on
	// purpose: a tool call's arguments are cut at 4 KiB when the trajectory
	// stores them, and a values document that did not survive that cut would be
	// a change nobody could read back in full afterwards. The bound keeps the
	// recorded call and the executed change the same thing. A configuration
	// larger than this belongs in the Helm application, where a person edits it
	// against the chart's own schema.
	maxHelmValuesBytes = 3 << 10
	// How long a release change may wait for its objects to settle. Helm allows
	// an hour; a tool call runs inside one AIOps turn, which does not, so a
	// wait that outlives the turn is not a wait anybody will read the end of.
	defaultHelmWaitSeconds uint32 = 300
	maxHelmWaitSeconds     uint32 = 600
	// The description recorded on the revision itself, so `helm history` run
	// outside ZKE shows where the change came from. The Console's own routes
	// put the operator's username here; this path has their user id and not
	// their name, and a UUID in `helm history` names nobody — the audit row and
	// the session trail are where the operator is identified.
	helmChangeDescription = "ZKE AIOps"
)

// HelmReleaseWriter is the existing release lifecycle, unchanged.
//
// AIOps opens no second write path: the Server still fetches the chart from the
// curated catalogue, and the target Cluster's Agent still renders and applies
// it with Helm's own engine. What the catalogue adds is who may ask.
type HelmReleaseWriter interface {
	Install(context.Context, helm.InstallInput) (helmrelease.Report, error)
	Upgrade(context.Context, helm.UpgradeInput) (helmrelease.Report, error)
	Rollback(context.Context, helm.RollbackInput) (helmrelease.Report, error)
	Uninstall(context.Context, helm.UninstallInput) (helmrelease.Report, error)
}

// helmChange is one requested release change, already decoded and bounded.
//
// One struct for four actions because the snapshot has to be replayable
// verbatim after an approval: the executing tool rebuilds the same input from
// the same fields, and a per-action snapshot type would make "did the approved
// thing and the executed thing match" a question asked four times.
type helmChange struct {
	action    helmAction
	namespace string
	name      string
	// Install and upgrade.
	repositoryID    string
	chart           string
	version         string
	values          string
	createNamespace bool
	reuseValues     bool
	// Rollback.
	revision int64
	// Uninstall.
	keepHistory bool
	// All four.
	wait           bool
	timeoutSeconds uint32
}

type helmPreview struct {
	owner        string
	clusterID    string
	change       helmChange
	target       *aisession.Target
	expiresAt    time.Time
	executionKey string
	executing    bool
	result       *airuntime.ToolResult
}

type helmInstallArguments struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	RepositoryID    string `json:"repository_id"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Values          string `json:"values"`
	CreateNamespace bool   `json:"create_namespace"`
	Wait            bool   `json:"wait"`
	TimeoutSeconds  uint32 `json:"timeout_seconds"`
}

type helmUpgradeArguments struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	RepositoryID   string `json:"repository_id"`
	Chart          string `json:"chart"`
	Version        string `json:"version"`
	Values         string `json:"values"`
	ReuseValues    bool   `json:"reuse_values"`
	Wait           bool   `json:"wait"`
	TimeoutSeconds uint32 `json:"timeout_seconds"`
}

type helmRollbackArguments struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Revision       int64  `json:"revision"`
	Wait           bool   `json:"wait"`
	TimeoutSeconds uint32 `json:"timeout_seconds"`
}

type helmUninstallArguments struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	KeepHistory    bool   `json:"keep_history"`
	Wait           bool   `json:"wait"`
	TimeoutSeconds uint32 `json:"timeout_seconds"`
}

func (catalogue *Catalogue) previewHelmInstall(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmInstallArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	change := helmChange{
		action: helmActionInstall, namespace: arguments.Namespace, name: arguments.Name,
		repositoryID: arguments.RepositoryID, chart: arguments.Chart,
		version: arguments.Version, values: arguments.Values,
		createNamespace: arguments.CreateNamespace,
		wait:            arguments.Wait, timeoutSeconds: arguments.TimeoutSeconds,
	}
	return catalogue.previewHelmChange(ctx, invocation, change)
}

func (catalogue *Catalogue) previewHelmUpgrade(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmUpgradeArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if arguments.ReuseValues && strings.TrimSpace(arguments.Values) != "" {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: reuse_values 与 values 不能同时使用；要在保留原值的基础上改动，请提交完整的新 values",
			airuntime.ErrInvalidInput)
	}
	change := helmChange{
		action: helmActionUpgrade, namespace: arguments.Namespace, name: arguments.Name,
		repositoryID: arguments.RepositoryID, chart: arguments.Chart,
		version: arguments.Version, values: arguments.Values,
		reuseValues: arguments.ReuseValues,
		wait:        arguments.Wait, timeoutSeconds: arguments.TimeoutSeconds,
	}
	return catalogue.previewHelmChange(ctx, invocation, change)
}

func (catalogue *Catalogue) previewHelmRollback(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmRollbackArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if arguments.Revision <= 0 {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: revision 必须大于 0；请先用 list_helm_release_revisions 选择一个 current=false 的版本",
			airuntime.ErrInvalidInput)
	}
	change := helmChange{
		action: helmActionRollback, namespace: arguments.Namespace, name: arguments.Name,
		revision: arguments.Revision,
		wait:     arguments.Wait, timeoutSeconds: arguments.TimeoutSeconds,
	}
	return catalogue.previewHelmChange(ctx, invocation, change)
}

func (catalogue *Catalogue) previewHelmUninstall(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmUninstallArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	change := helmChange{
		action: helmActionUninstall, namespace: arguments.Namespace, name: arguments.Name,
		keepHistory: arguments.KeepHistory,
		wait:        arguments.Wait, timeoutSeconds: arguments.TimeoutSeconds,
	}
	return catalogue.previewHelmChange(ctx, invocation, change)
}

// previewHelmChange authorizes the change, runs Helm's own dry run and stores
// the snapshot the executing tool will replay.
func (catalogue *Catalogue) previewHelmChange(
	ctx context.Context, invocation airuntime.ToolInvocation, change helmChange,
) (airuntime.ToolResult, error) {
	if err := change.validate(); err != nil {
		return airuntime.ToolResult{}, err
	}
	target := change.target()
	allowClusterScoped, missing, err := catalogue.authorizeHelmChange(ctx, invocation, change)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if missing != "" {
		return deniedClusterMutation(missing, target), nil
	}
	report, err := catalogue.runHelmChange(
		ctx, invocation.ClusterID, change, true, allowClusterScoped,
		invocation.IdempotencyKey+":dryrun",
	)
	if err != nil {
		if result, handled := helmChangeFailure(err, change.action, target); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	previewID, err := newHelmPreviewID(change.action)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	catalogue.storeHelmPreview(previewID, &helmPreview{
		owner: invocation.UserID, clusterID: invocation.ClusterID,
		change: change, target: target,
		// The same TTL every other preview snapshot on this Server uses. It is
		// one question — how long a checked-but-unapproved change stays
		// submittable — and answering it twice would let the two drift.
		expiresAt: time.Now().Add(catalogue.config.ManifestPreviewTTL),
	})
	return catalogue.helmChangeResult(invocation.ClusterID, change, report, previewID, target), nil
}

func (catalogue *Catalogue) applyHelmChange(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var reference previewReference
	if err := decode(invocation.Arguments, &reference); err != nil {
		return airuntime.ToolResult{}, err
	}
	preview, cached, err := catalogue.reserveHelmPreview(reference.PreviewID, invocation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if cached != nil {
		return *cached, nil
	}
	succeeded := false
	defer func() { catalogue.releaseHelmPreview(reference.PreviewID, succeeded) }()

	// Rechecked after the approval rather than only before it: an operator can
	// lose a permission while a request is parked on somebody, and the snapshot
	// is not a grant.
	allowClusterScoped, missing, err := catalogue.authorizeHelmChange(ctx, invocation, preview.change)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if missing != "" {
		return deniedClusterMutation(missing, preview.target), nil
	}
	// The same dry run again, against the Cluster as it is now. The preview an
	// operator approved described a Cluster that may have moved since.
	if _, err := catalogue.runHelmChange(
		ctx, invocation.ClusterID, preview.change, true, allowClusterScoped,
		preview.executionKey+":preflight",
	); err != nil {
		if result, handled := helmChangeFailure(err, preview.change.action, preview.target); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	report, err := catalogue.runHelmChange(
		ctx, invocation.ClusterID, preview.change, false, allowClusterScoped,
		preview.executionKey,
	)
	if err != nil {
		if result, handled := helmChangeFailure(err, preview.change.action, preview.target); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	result := catalogue.helmChangeResult(
		invocation.ClusterID, preview.change, report, "", preview.target)
	succeeded = true
	// Cached against the preview so a repeated call answers with what the first
	// one did rather than running a second release change.
	catalogue.completeHelmPreview(reference.PreviewID, result)
	return result, nil
}

func (change helmChange) validate() error {
	if len(change.values) > maxHelmValuesBytes {
		return fmt.Errorf("%w: values 最多 %d 字节", airuntime.ErrInvalidInput, maxHelmValuesBytes)
	}
	if change.timeoutSeconds > maxHelmWaitSeconds {
		return fmt.Errorf(
			"%w: timeout_seconds 最多 %d", airuntime.ErrInvalidInput, maxHelmWaitSeconds)
	}
	switch change.action {
	case helmActionInstall, helmActionUpgrade:
		if strings.TrimSpace(change.repositoryID) == "" || strings.TrimSpace(change.chart) == "" {
			return fmt.Errorf(
				"%w: repository_id 与 chart 必填；Chart 只能来自平台维护的仓库目录",
				airuntime.ErrInvalidInput)
		}
	}
	return nil
}

func (change helmChange) target() *aisession.Target {
	return &aisession.Target{Namespace: change.namespace, Name: change.name}
}

// waitSeconds is how long the Agent may wait, and only when a wait was asked
// for. Zero means Helm returns as soon as the objects are written.
func (change helmChange) waitSeconds() uint32 {
	if !change.wait {
		return 0
	}
	if change.timeoutSeconds == 0 {
		return defaultHelmWaitSeconds
	}
	return change.timeoutSeconds
}

// runHelmChange hands the change to the existing release service.
//
// A dry run never waits: there is nothing to wait for, and a wait would spend
// the turn's budget on a rollout that is not happening.
func (catalogue *Catalogue) runHelmChange(
	ctx context.Context,
	clusterID string,
	change helmChange,
	dryRun bool,
	allowClusterScoped bool,
	idempotencyKey string,
) (helmrelease.Report, error) {
	wait := change.wait && !dryRun
	timeout := uint32(0)
	if wait {
		timeout = change.waitSeconds()
	}
	switch change.action {
	case helmActionInstall, helmActionUpgrade:
		input := helm.InstallInput{
			ClusterID: clusterID, Namespace: change.namespace, Name: change.name,
			RepositoryID: change.repositoryID, Chart: change.chart, Version: change.version,
			Values: change.values, DryRun: dryRun,
			// Passed through on a dry run too: a preview that refused to create
			// the Namespace would fail for exactly the install that needs it,
			// and Helm's dry run writes nothing either way.
			CreateNamespace:    change.createNamespace,
			Wait:               wait,
			TimeoutSeconds:     timeout,
			Description:        helmChangeDescription,
			AllowClusterScoped: allowClusterScoped,
			IdempotencyKey:     idempotencyKey,
		}
		if change.action == helmActionInstall {
			return catalogue.dependencies.HelmWrites.Install(ctx, input)
		}
		input.CreateNamespace = false
		return catalogue.dependencies.HelmWrites.Upgrade(ctx, helm.UpgradeInput{
			InstallInput: input, ReuseValues: change.reuseValues,
		})
	case helmActionRollback:
		return catalogue.dependencies.HelmWrites.Rollback(ctx, helm.RollbackInput{
			ClusterID: clusterID, Namespace: change.namespace, Name: change.name,
			Revision: change.revision, DryRun: dryRun, Wait: wait,
			TimeoutSeconds:     timeout,
			Description:        helmChangeDescription,
			AllowClusterScoped: allowClusterScoped,
			IdempotencyKey:     idempotencyKey,
		})
	default:
		return catalogue.dependencies.HelmWrites.Uninstall(ctx, helm.UninstallInput{
			ClusterID: clusterID, Namespace: change.namespace, Name: change.name,
			KeepHistory: change.keepHistory, DryRun: dryRun, Wait: wait,
			TimeoutSeconds:     timeout,
			Description:        helmChangeDescription,
			AllowClusterScoped: allowClusterScoped,
			IdempotencyKey:     idempotencyKey,
		})
	}
}

// authorizeHelmChange asks every question a release change raises, in one
// resolution of the Cluster's scope.
//
// It returns the first permission the operator does not hold rather than a
// boolean: a refusal that says which grant is missing is one somebody can act
// on, and it is what the trail and the audit row record.
func (catalogue *Catalogue) authorizeHelmChange(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
	change helmChange,
) (allowClusterScoped bool, missing rbac.Permission, err error) {
	scope, err := catalogue.dependencies.Scopes.ResolveClusterScope(ctx, invocation.ClusterID)
	if err != nil {
		return false, "", err
	}
	holds := func(permission rbac.Permission) (bool, error) {
		err := catalogue.dependencies.Scopes.AuthorizeResolvedCluster(
			ctx, invocation.UserID, permission, scope)
		if errors.Is(err, rbac.ErrDenied) {
			return false, nil
		}
		return err == nil, err
	}
	// The object permissions this action actually spends. An install, an
	// upgrade and a rollback all create what is missing and replace what
	// changed; an uninstall removes.
	required := []rbac.Permission{
		rbac.PermissionClusterResourceCreate, rbac.PermissionClusterResourceUpdate,
	}
	if change.action == helmActionUninstall {
		required = []rbac.Permission{rbac.PermissionClusterResourceDelete}
	}
	// A release in the Agent's own Namespace or a `kube-` one needs the same
	// additional grant any other write there needs. `default` is not protected
	// here, exactly as it is not on the Console's release routes.
	switch {
	case scope.AgentNamespace != "" && change.namespace == scope.AgentNamespace:
		required = append(required, rbac.PermissionClusterAgentNamespaceManage)
	case strings.HasPrefix(change.namespace, "kube-"):
		required = append(required, rbac.PermissionClusterSystemNamespaceManage)
	}
	for _, permission := range required {
		granted, err := holds(permission)
		if err != nil {
			return false, "", err
		}
		if !granted {
			return false, permission, nil
		}
	}
	// Not required, and never taken from an argument: it only decides whether
	// the Agent will accept a chart that renders an object no Namespace
	// contains. Without it such a chart is refused by name.
	allowClusterScoped, err = holds(rbac.PermissionClusterManage)
	if err != nil {
		return false, "", err
	}
	return allowClusterScoped, "", nil
}

// helmChangeResult renders a report without its Secret content.
//
// The manifest and NOTES.txt a release produces are the same Secret content the
// read tools refuse, so they are reduced here to the identities of the objects
// the change touches — which is what an approver is actually being asked about.
func (catalogue *Catalogue) helmChangeResult(
	clusterID string,
	change helmChange,
	report helmrelease.Report,
	previewID string,
	target *aisession.Target,
) airuntime.ToolResult {
	objects, partial := helmManifestObjects(report.Manifest)
	rendered := make([]string, 0, len(objects))
	for _, object := range objects {
		rendered = append(rendered, renderedObjectLine(object))
	}
	digest := map[string]any{
		"action":           string(change.action),
		"dry_run":          report.DryRun,
		"namespace":        report.Namespace,
		"name":             report.Name,
		"revision":         report.Revision,
		"status":           report.Status,
		"description":      report.Description,
		"chart_name":       report.ChartName,
		"chart_version":    report.ChartVersion,
		"app_version":      report.AppVersion,
		"deleted":          report.Deleted,
		"affected_objects": rendered,
		"affected_partial": partial || report.ManifestTruncated,
		"omitted": "渲染后的 Manifest 正文与 NOTES.txt 属于 Secret 内容，不会进入 AIOps 上下文；" +
			"需要逐字审阅时请在 ZKE 的 Helm 应用中打开。",
	}
	if previewID != "" {
		digest["preview_id"] = previewID
	}
	header := helmChangeHeadline(change, report, previewID)
	evidence := []aisession.Evidence{{
		Kind: aisession.EvidenceHelmRelease, Cluster: clusterID,
		Namespace: report.Namespace, Name: report.Name,
	}}
	for _, object := range objects {
		if len(evidence) >= maxHelmEvidence {
			break
		}
		evidence = append(evidence, aisession.Evidence{
			Kind: aisession.EvidenceResource, Cluster: clusterID,
			Namespace: object.namespace,
			GVK:       groupVersionKind(object.apiVersion, object.kind),
			Name:      object.name,
		})
	}
	// No AuditTargets: this tool touches exactly one release, and the runtime
	// already records that one target. A row per rendered object would claim
	// the change was authorized object by object, and it was not — it was
	// authorized as a release change.
	return airuntime.ToolResult{
		Text: header + "\n" + catalogue.encode(digest), Evidence: evidence, Target: target,
	}
}

func helmChangeHeadline(
	change helmChange, report helmrelease.Report, previewID string,
) string {
	action := map[helmAction]string{
		helmActionInstall:   "安装",
		helmActionUpgrade:   "升级",
		helmActionRollback:  "回滚",
		helmActionUninstall: "卸载",
	}[change.action]
	if report.DryRun {
		return fmt.Sprintf(
			"已完成 %s/%s 的 Helm %s 预检（DryRun，集群未改变）。批准后请用 apply_helm_release_change 提交 preview_id=%s。",
			change.namespace, change.name, action, previewID,
		)
	}
	return fmt.Sprintf(
		"已提交 %s/%s 的 Helm %s，Release 当前 revision %d、状态 %s。",
		report.Namespace, report.Name, action, report.Revision, report.Status,
	)
}

// helmChangeFailure keeps the outcomes that are about this change rather than
// about the Cluster being unreachable.
//
// The Agent's own words are passed through for a refusal, because "a release
// with this name already exists" and "the Agent is offline" lead to completely
// different next steps and the runtime's generic text would report both as the
// second one.
func helmChangeFailure(
	err error, action helmAction, target *aisession.Target,
) (airuntime.ToolResult, bool) {
	result := airuntime.ToolResult{Target: target, Failed: true}
	var rejection *helm.ReleaseRejection
	switch {
	case errors.As(err, &rejection):
		result.Text = "目标 Cluster 拒绝了这次 Helm 操作：" + helmRejectionText(rejection)
	// A repository that is not in the catalogue, and a `repository_id` that is
	// not an identifier at all, both land here. Left to the runtime's generic
	// text they read as "the Agent may be unreachable", which is wrong twice:
	// no Agent has been contacted yet, and the fix is to list the catalogue
	// rather than to retry.
	case errors.Is(err, store.ErrHelmRepositoryNotFound):
		result.Text = "没有这个 Chart 仓库。repository_id 是平台目录里的标识，" +
			"请先调用 list_helm_repositories 取得它。"
	case errors.Is(err, helm.ErrInvalidInput):
		if action == helmActionInstall || action == helmActionUpgrade {
			result.Text = "请求参数不合法：repository_id 必须是 list_helm_repositories 返回的标识（不是仓库名称），" +
				"Namespace 与 Release 名称必须是合法的 Kubernetes 名称。未执行。"
			break
		}
		result.Text = "请求参数不合法：Namespace 与 Release 名称必须是合法的 Kubernetes 名称。未执行。"
	case errors.Is(err, helm.ErrValuesRejected):
		result.Text = "values 不满足该 Chart 自带的 values.schema.json，未执行。请按 Chart 的 values 说明修正。"
	case errors.Is(err, helm.ErrChartNotFound):
		result.Text = "仓库目录里没有这个 Chart 或这个版本，未执行。可以先确认 repository_id、chart 与 version。"
	case errors.Is(err, helm.ErrRepositoryDisabled):
		result.Text = "该 Chart 仓库已被平台管理员停用，未执行。"
	case errors.Is(err, helm.ErrChartUnsigned), errors.Is(err, helm.ErrChartSignatureInvalid):
		result.Text = "该仓库要求 Chart 带可校验的来源证明，这个版本没有通过校验，未执行。"
	case errors.Is(err, helm.ErrChartTooLarge):
		result.Text = "Chart 归档超过可传输大小，未执行。"
	case errors.Is(err, helm.ErrChartOCIUnsupported):
		result.Text = "该 Chart 只发布在 OCI registry 上，ZKE 不从这里读取，未执行。"
	case errors.Is(err, helm.ErrRepositoryUnreachable):
		result.Text = "Chart 仓库暂时读不到，未执行。这不是集群问题，稍后可以重试。"
	case errors.Is(err, agentconn.ErrHelmCapabilityMissing), errors.Is(err, helm.ErrUnsupported):
		result.Text = "目标 Cluster 的 Agent 版本过低，不支持 Helm Release 管理，未执行。"
	case errors.Is(err, agentconn.ErrHelmRequestExhausted):
		result.Text = "该 Cluster 已经有一个 Helm 操作在执行，未执行。Helm 的存储没有锁，同一集群同一时刻只跑一个。"
	case errors.Is(err, helm.ErrReportUnreadable):
		// The change happened; only the account of it is missing. Reporting a
		// failure here would send the model to redo a write that already ran.
		result.Text = "Helm 操作已经执行，但它的报告无法解码。请用 list_helm_release_revisions 确认实际结果，不要直接重试。"
	default:
		return airuntime.ToolResult{}, false
	}
	return result, true
}

func helmRejectionText(rejection *helm.ReleaseRejection) string {
	if message := strings.TrimSpace(rejection.Message); message != "" {
		return message
	}
	if reason := strings.TrimSpace(rejection.Reason); reason != "" {
		return reason
	}
	return "集群没有给出具体原因。"
}

// renderedObjectLine names one object the way kubectl does rather than as the
// slashed GVK the evidence carries: this line is read by a person as often as
// by the model, and `apps/v1/Deployment web/shop` reads as three path segments
// rather than as a kind and an object.
func renderedObjectLine(object helmRenderedObject) string {
	if object.namespace != "" {
		return fmt.Sprintf(
			"%s %s %s/%s", object.apiVersion, object.kind, object.namespace, object.name)
	}
	return fmt.Sprintf("%s %s %s", object.apiVersion, object.kind, object.name)
}

// --- Preview snapshots -------------------------------------------------------

// newHelmPreviewID names the action inside the identifier.
//
// One executing tool serves all four actions, so its arguments are a single
// opaque string — and that string is the whole of what an approver is shown in
// the approval prompt. `helm_uninstall_…` and `helm_upgrade_…` are not the same
// request to approve, and the prefix is the cheapest place to say which one it
// is without splitting the tool four ways in every request's catalogue.
func newHelmPreviewID(action helmAction) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return fmt.Sprintf("helm_%s_%s", action, hex.EncodeToString(value)), nil
}

func (catalogue *Catalogue) storeHelmPreview(id string, preview *helmPreview) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	now := time.Now()
	for key, item := range catalogue.helmChanges {
		if now.After(item.expiresAt) {
			delete(catalogue.helmChanges, key)
		}
	}
	if len(catalogue.helmChanges) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, item := range catalogue.helmChanges {
			if oldestKey == "" || item.expiresAt.Before(oldest) {
				oldestKey, oldest = key, item.expiresAt
			}
		}
		delete(catalogue.helmChanges, oldestKey)
	}
	catalogue.helmChanges[id] = preview
}

func (catalogue *Catalogue) reserveHelmPreview(
	id string, invocation airuntime.ToolInvocation,
) (*helmPreview, *airuntime.ToolResult, error) {
	unknown := fmt.Errorf(
		"%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	if !strings.HasPrefix(id, "helm_") {
		return nil, nil, fmt.Errorf("%w: preview_id 无效", airuntime.ErrInvalidInput)
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	preview := catalogue.helmChanges[id]
	if preview == nil {
		return nil, nil, unknown
	}
	if time.Now().After(preview.expiresAt) {
		delete(catalogue.helmChanges, id)
		return nil, nil, unknown
	}
	if preview.owner != invocation.UserID || preview.clusterID != invocation.ClusterID ||
		!strings.HasPrefix(id, "helm_"+string(preview.change.action)+"_") {
		return nil, nil, unknown
	}
	if preview.result != nil {
		cached := *preview.result
		return preview, &cached, nil
	}
	if preview.executing {
		return nil, nil, fmt.Errorf("%w: 该预检正在提交", airuntime.ErrInvalidInput)
	}
	preview.executing = true
	if preview.executionKey == "" {
		preview.executionKey = invocation.IdempotencyKey
	}
	return preview, nil, nil
}

func (catalogue *Catalogue) releaseHelmPreview(id string, succeeded bool) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.helmChanges[id]; preview != nil {
		preview.executing = false
		if succeeded {
			preview.expiresAt = time.Now().Add(catalogue.config.ManifestPreviewTTL)
		}
	}
}

func (catalogue *Catalogue) completeHelmPreview(id string, result airuntime.ToolResult) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.helmChanges[id]; preview != nil {
		stored := result
		preview.result = &stored
	}
}

// helmChangeTarget names the release an approval prompt is about, read from the
// snapshot rather than from the argument — the argument is only an identifier.
func (catalogue *Catalogue) helmChangeTarget(arguments json.RawMessage) *aisession.Target {
	var reference previewReference
	if decode(arguments, &reference) != nil {
		return nil
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	preview := catalogue.helmChanges[reference.PreviewID]
	if preview == nil || preview.target == nil || time.Now().After(preview.expiresAt) {
		return nil
	}
	target := *preview.target
	return &target
}

// The preview tools name their target from their own arguments, so the
// approval prompt, the trajectory and the audit row say which release is about
// to be previewed before the call runs.
func helmInstallTarget(arguments json.RawMessage) *aisession.Target {
	var input helmInstallArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{Namespace: input.Namespace, Name: input.Name}
}

func helmUpgradeTarget(arguments json.RawMessage) *aisession.Target {
	var input helmUpgradeArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{Namespace: input.Namespace, Name: input.Name}
}

func helmRollbackTarget(arguments json.RawMessage) *aisession.Target {
	var input helmRollbackArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{Namespace: input.Namespace, Name: input.Name}
}

func helmUninstallTarget(arguments json.RawMessage) *aisession.Target {
	var input helmUninstallArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{Namespace: input.Namespace, Name: input.Name}
}
