package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/helm"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

// helmCall is one thing the tools asked the release service to do. The tests
// assert against these rather than against text, because what matters is the
// request that reached the Cluster.
type helmCall struct {
	action             helmAction
	dryRun             bool
	namespace          string
	name               string
	values             string
	revision           int64
	keepHistory        bool
	reuseValues        bool
	wait               bool
	timeoutSeconds     uint32
	allowClusterScoped bool
	idempotencyKey     string
}

type recordingHelmWriter struct {
	calls  []helmCall
	report helmrelease.Report
	err    error
}

func (writer *recordingHelmWriter) record(call helmCall) (helmrelease.Report, error) {
	writer.calls = append(writer.calls, call)
	if writer.err != nil {
		return helmrelease.Report{}, writer.err
	}
	report := writer.report
	report.DryRun = call.dryRun
	if report.Namespace == "" {
		report.Namespace = call.namespace
	}
	if report.Name == "" {
		report.Name = call.name
	}
	return report, nil
}

func (writer *recordingHelmWriter) Install(
	_ context.Context, input helm.InstallInput,
) (helmrelease.Report, error) {
	return writer.record(helmCall{
		action: helmActionInstall, dryRun: input.DryRun,
		namespace: input.Namespace, name: input.Name, values: input.Values,
		wait: input.Wait, timeoutSeconds: input.TimeoutSeconds,
		allowClusterScoped: input.AllowClusterScoped, idempotencyKey: input.IdempotencyKey,
	})
}

func (writer *recordingHelmWriter) Upgrade(
	_ context.Context, input helm.UpgradeInput,
) (helmrelease.Report, error) {
	return writer.record(helmCall{
		action: helmActionUpgrade, dryRun: input.DryRun,
		namespace: input.Namespace, name: input.Name, values: input.Values,
		reuseValues: input.ReuseValues, wait: input.Wait, timeoutSeconds: input.TimeoutSeconds,
		allowClusterScoped: input.AllowClusterScoped, idempotencyKey: input.IdempotencyKey,
	})
}

func (writer *recordingHelmWriter) Rollback(
	_ context.Context, input helm.RollbackInput,
) (helmrelease.Report, error) {
	return writer.record(helmCall{
		action: helmActionRollback, dryRun: input.DryRun,
		namespace: input.Namespace, name: input.Name, revision: input.Revision,
		wait: input.Wait, timeoutSeconds: input.TimeoutSeconds,
		allowClusterScoped: input.AllowClusterScoped, idempotencyKey: input.IdempotencyKey,
	})
}

func (writer *recordingHelmWriter) Uninstall(
	_ context.Context, input helm.UninstallInput,
) (helmrelease.Report, error) {
	return writer.record(helmCall{
		action: helmActionUninstall, dryRun: input.DryRun,
		namespace: input.Namespace, name: input.Name, keepHistory: input.KeepHistory,
		wait: input.Wait, timeoutSeconds: input.TimeoutSeconds,
		allowClusterScoped: input.AllowClusterScoped, idempotencyKey: input.IdempotencyKey,
	})
}

// helmWriteGrants is the full stack an install or an upgrade spends, minus the
// two a test wants to take away one at a time.
func helmWriteGrants(extra ...rbac.Permission) permissionScope {
	allowed := map[rbac.Permission]bool{
		rbac.PermissionClusterRead:           true,
		rbac.PermissionClusterHelmManage:     true,
		rbac.PermissionClusterSecretManage:   true,
		rbac.PermissionClusterResourceCreate: true,
		rbac.PermissionClusterResourceUpdate: true,
		rbac.PermissionClusterResourceDelete: true,
	}
	for _, permission := range extra {
		allowed[permission] = true
	}
	return permissionScope{staticScopeResolver: ordinaryScope(), allowed: allowed}
}

func helmWriteCatalogue(
	writer *recordingHelmWriter, scope permissionScope,
) *Catalogue {
	return New(Dependencies{HelmWrites: writer, Scopes: scope}, Config{})
}

func helmWriteInvocation(name, arguments string) airuntime.ToolInvocation {
	return airuntime.ToolInvocation{
		Name: name, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(arguments),
		IdempotencyKey: "aiops:0123456789abcdef0123456789abcdef",
	}
}

func helmReport() helmrelease.Report {
	return helmrelease.Report{
		Name: "shop", Namespace: "web", Revision: 3, Status: "deployed",
		ChartName: "shop", ChartVersion: "1.4.2",
		Notes: "the password is hunter2-in-the-notes",
		Manifest: "apiVersion: apps/v1\nkind: Deployment\n" +
			"metadata:\n  name: shop\n  namespace: web\n" +
			"---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: shop-auth\n  namespace: web\n" +
			"data:\n  password: aHVudGVyMi1pbi10aGUtbWFuaWZlc3Q=\n",
	}
}

// The three permissions every release change spends whatever it does are
// static, so the runtime rechecks them before every call. The object
// permissions are not, because an install and an uninstall do not spend the
// same ones — those are resolved inside the tool.
func TestHelmWriteToolsRequireTheReleaseWritePermissions(t *testing.T) {
	t.Parallel()
	specs := helmWriteCatalogue(&recordingHelmWriter{}, helmWriteGrants()).Specs()
	for _, name := range []string{
		toolPreviewHelmInstall, toolPreviewHelmUpgrade, toolPreviewHelmRollback,
		toolPreviewHelmUninstall, toolApplyHelmChange,
	} {
		spec, found := findHelmSpec(specs, name)
		if !found {
			t.Fatalf("%s is not in the catalogue", name)
		}
		holds := map[rbac.Permission]bool{}
		for _, permission := range spec.Permissions {
			holds[permission] = true
		}
		for _, required := range []rbac.Permission{
			rbac.PermissionClusterRead,
			rbac.PermissionClusterHelmManage,
			rbac.PermissionClusterSecretManage,
		} {
			if !holds[required] {
				t.Fatalf("%s does not require %s: %v", name, required, spec.Permissions)
			}
		}
	}
}

// A preview writes nothing, so it does not stop for a person. Submitting one
// always does: a release change writes every object an application owns, and
// there is no version of that which is routine.
func TestOnlyTheSubmittingHelmToolIsMutatingAndSensitive(t *testing.T) {
	t.Parallel()
	specs := helmWriteCatalogue(&recordingHelmWriter{}, helmWriteGrants()).Specs()
	for _, name := range []string{
		toolPreviewHelmInstall, toolPreviewHelmUpgrade, toolPreviewHelmRollback,
		toolPreviewHelmUninstall,
	} {
		spec, _ := findHelmSpec(specs, name)
		if spec.Mutating || spec.Sensitive {
			t.Fatalf("%s is marked mutating=%t sensitive=%t; a DryRun is neither",
				name, spec.Mutating, spec.Sensitive)
		}
	}
	apply, _ := findHelmSpec(specs, toolApplyHelmChange)
	if !apply.Mutating || !apply.Sensitive {
		t.Fatalf("%s mutating=%t sensitive=%t", toolApplyHelmChange, apply.Mutating, apply.Sensitive)
	}
}

func TestHelmWriteToolsAreAbsentWithoutTheWriter(t *testing.T) {
	t.Parallel()
	for _, spec := range New(Dependencies{Scopes: ordinaryScope()}, Config{}).Specs() {
		switch spec.Name {
		case toolPreviewHelmInstall, toolPreviewHelmUpgrade, toolPreviewHelmRollback,
			toolPreviewHelmUninstall, toolApplyHelmChange:
			t.Fatalf("catalogue advertises %s without a Helm writer", spec.Name)
		}
	}
}

// The whole flow: a preview runs Helm's dry run and hands back a snapshot id,
// and submitting it re-runs the dry run before the real change. The second dry
// run is not ceremony — the Cluster an operator approved a preview against can
// have moved while the approval was waiting.
func TestPreviewThenApplyRunsADryRunAgainBeforeTheRealChange(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())

	preview, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUpgrade,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop","version":"1.4.2","reuse_values":true}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	previewID := helmPreviewIDFrom(t, preview.Text)
	if !strings.HasPrefix(previewID, "helm_upgrade_") {
		t.Fatalf("preview id does not name its action: %q", previewID)
	}

	applied, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolApplyHelmChange, `{"preview_id":"`+previewID+`"}`,
	))
	if err != nil {
		t.Fatalf("apply error = %v", err)
	}
	if applied.Failed {
		t.Fatalf("apply failed: %s", applied.Text)
	}
	if len(writer.calls) != 3 {
		t.Fatalf("calls = %+v", writer.calls)
	}
	if !writer.calls[0].dryRun || !writer.calls[1].dryRun || writer.calls[2].dryRun {
		t.Fatalf("dry run sequence = %+v", writer.calls)
	}
	for _, call := range writer.calls {
		if call.action != helmActionUpgrade || call.namespace != "web" ||
			call.name != "shop" || !call.reuseValues {
			t.Fatalf("apply did not replay the previewed change: %+v", call)
		}
	}
	// The submitted change and its preflight must not share an idempotency key,
	// or a retried write would be answered by the preflight's record.
	if writer.calls[2].idempotencyKey == writer.calls[1].idempotencyKey {
		t.Fatalf("preflight and submission share an idempotency key: %+v", writer.calls)
	}
}

// A second submission of the same snapshot answers with what the first one did
// rather than running a second release change.
func TestApplyingTheSameSnapshotTwiceDoesNotChangeTheClusterTwice(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())
	preview, _ := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUninstall, `{"namespace":"web","name":"shop"}`,
	))
	previewID := helmPreviewIDFrom(t, preview.Text)
	invocation := helmWriteInvocation(toolApplyHelmChange, `{"preview_id":"`+previewID+`"}`)

	if _, err := catalogue.Invoke(context.Background(), invocation); err != nil {
		t.Fatalf("first apply error = %v", err)
	}
	before := len(writer.calls)
	if _, err := catalogue.Invoke(context.Background(), invocation); err != nil {
		t.Fatalf("second apply error = %v", err)
	}
	if len(writer.calls) != before {
		t.Fatalf("the second apply reached the Cluster: %+v", writer.calls[before:])
	}
}

// An uninstall spends the delete permission and only that one; an operator who
// may create and update but not delete may not uninstall.
func TestUninstallRequiresDeleteRatherThanCreateAndUpdate(t *testing.T) {
	t.Parallel()
	scope := permissionScope{staticScopeResolver: ordinaryScope(), allowed: map[rbac.Permission]bool{
		rbac.PermissionClusterRead:           true,
		rbac.PermissionClusterHelmManage:     true,
		rbac.PermissionClusterSecretManage:   true,
		rbac.PermissionClusterResourceCreate: true,
		rbac.PermissionClusterResourceUpdate: true,
	}}
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, scope)

	result, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUninstall, `{"namespace":"web","name":"shop"}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	if !result.Denied || !strings.Contains(result.Text, string(rbac.PermissionClusterResourceDelete)) {
		t.Fatalf("preview = %+v", result)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("a refused uninstall still reached the Cluster: %+v", writer.calls)
	}
}

// A release in the Agent's own Namespace needs the same additional grant any
// other write there needs. It is refused before the Cluster is contacted.
func TestAProtectedNamespaceNeedsItsOwnGrant(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())

	result, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"zke-system","name":"shop","repository_id":"repo-1","chart":"shop"}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	if !result.Denied ||
		!strings.Contains(result.Text, string(rbac.PermissionClusterAgentNamespaceManage)) {
		t.Fatalf("preview = %+v", result)
	}

	granted := helmWriteCatalogue(
		&recordingHelmWriter{report: helmReport()},
		helmWriteGrants(rbac.PermissionClusterAgentNamespaceManage),
	)
	allowed, err := granted.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"zke-system","name":"shop","repository_id":"repo-1","chart":"shop"}`,
	))
	if err != nil || allowed.Denied {
		t.Fatalf("granted preview = %+v, err = %v", allowed, err)
	}
}

// Whether a chart may render objects no Namespace contains is resolved from
// the operator's `cluster.manage`, never from an argument — the schema has no
// field for it and the answer travels to the Agent either way.
func TestClusterScopedObjectsFollowClusterManageAndNotAnArgument(t *testing.T) {
	t.Parallel()
	for _, granted := range []bool{false, true} {
		scope := helmWriteGrants()
		if granted {
			scope = helmWriteGrants(rbac.PermissionClusterManage)
		}
		writer := &recordingHelmWriter{report: helmReport()}
		if _, err := helmWriteCatalogue(writer, scope).Invoke(
			context.Background(),
			helmWriteInvocation(toolPreviewHelmInstall,
				`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop"}`),
		); err != nil {
			t.Fatalf("preview error = %v", err)
		}
		if len(writer.calls) != 1 || writer.calls[0].allowClusterScoped != granted {
			t.Fatalf("cluster.manage=%t produced %+v", granted, writer.calls)
		}
	}
	// And the schema does not let the model ask for it.
	result := New(Dependencies{
		HelmWrites: &recordingHelmWriter{report: helmReport()}, Scopes: helmWriteGrants(),
	}, Config{})
	if _, err := result.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop","allow_cluster_scoped":true}`,
	)); err == nil {
		t.Fatal("the tool accepted an allow_cluster_scoped argument")
	}
}

// A report is bounded the same way a release read is: the rendered manifest and
// NOTES.txt are Secret content, and what an approver needs is which objects the
// change touches.
func TestAChangeReportNamesItsObjectsAndNotItsSecretContent(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())

	result, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop"}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	for _, leaked := range []string{
		"hunter2-in-the-notes", "aHVudGVyMi1pbi10aGUtbWFuaWZlc3Q=",
	} {
		if strings.Contains(result.Text, leaked) {
			t.Fatalf("the report leaked Secret content %q:\n%s", leaked, result.Text)
		}
	}
	for _, wanted := range []string{
		"apps/v1 Deployment web/shop", "v1 Secret web/shop-auth", "\"dry_run\": true",
	} {
		if !strings.Contains(result.Text, wanted) {
			t.Fatalf("the report dropped %q:\n%s", wanted, result.Text)
		}
	}
}

// A snapshot belongs to the operator and the Cluster that made it. Another
// account holding a preview id is not an authorization.
func TestASnapshotIsNotTransferableToAnotherOperator(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())
	preview, _ := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUninstall, `{"namespace":"web","name":"shop"}`,
	))
	previewID := helmPreviewIDFrom(t, preview.Text)

	stolen := helmWriteInvocation(toolApplyHelmChange, `{"preview_id":"`+previewID+`"}`)
	stolen.UserID = "0f2f4a2c-f69c-43a3-b2d1-26d352e74ce8"
	if _, err := catalogue.Invoke(context.Background(), stolen); err == nil {
		t.Fatal("another operator submitted this snapshot")
	}
}

// Values are the one field a model authors freely, so they are bounded to what
// the trajectory records in full. A change nobody can read back afterwards is
// not a change anybody approved.
func TestValuesAreBoundedToWhatTheTrailKeeps(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())
	oversized, _ := json.Marshal(map[string]any{
		"namespace": "web", "name": "shop", "repository_id": "repo-1", "chart": "shop",
		"values": strings.Repeat("a", maxHelmValuesBytes+1),
	})

	if _, err := catalogue.Invoke(
		context.Background(),
		helmWriteInvocation(toolPreviewHelmInstall, string(oversized)),
	); err == nil {
		t.Fatal("an oversized values document was accepted")
	}
	if len(writer.calls) != 0 {
		t.Fatalf("it still reached the Cluster: %+v", writer.calls)
	}
}

// reuse_values merges the previous revision's values; sending a document as
// well makes "which one wins" a question with no good answer.
func TestUpgradeRefusesReuseValuesTogetherWithValues(t *testing.T) {
	t.Parallel()
	catalogue := helmWriteCatalogue(&recordingHelmWriter{report: helmReport()}, helmWriteGrants())
	if _, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUpgrade,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop","reuse_values":true,"values":"replicaCount: 2"}`,
	)); err == nil {
		t.Fatal("reuse_values and values were accepted together")
	}
}

// A rollback target has to be chosen from the history, so the tool refuses the
// "whatever came before" form rather than picking a revision itself.
func TestRollbackRequiresAnExplicitRevision(t *testing.T) {
	t.Parallel()
	catalogue := helmWriteCatalogue(&recordingHelmWriter{report: helmReport()}, helmWriteGrants())
	if _, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmRollback, `{"namespace":"web","name":"shop","revision":0}`,
	)); err == nil {
		t.Fatal("a rollback without a revision was accepted")
	}
}

// A Cluster that refuses the change said why, and that reason is what the model
// needs: "a release with this name already exists" and "the Agent is offline"
// lead to completely different next steps.
func TestAClusterRefusalIsReportedWithItsOwnReason(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{err: &helm.ReleaseRejection{
		Reason: "AlreadyExists", Message: "cannot re-use a name that is still in use",
	}}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())

	result, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop"}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	if !result.Failed || !strings.Contains(result.Text, "cannot re-use a name") {
		t.Fatalf("preview = %+v", result)
	}
}

// An operation whose report could not be decoded already happened. Reporting it
// as a failure would send the model to redo a write that ran.
func TestAnUnreadableReportDoesNotReadAsAChangeThatDidNotHappen(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{err: helm.ErrReportUnreadable}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())

	result, _ := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmUninstall, `{"namespace":"web","name":"shop"}`,
	))
	if !strings.Contains(result.Text, "不要直接重试") {
		t.Fatalf("preview = %+v", result)
	}
}

// A dry run has nothing to wait for, and waiting would spend the turn's budget
// on a rollout that is not happening.
func TestADryRunNeverWaits(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{report: helmReport()}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())
	preview, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmInstall,
		`{"namespace":"web","name":"shop","repository_id":"repo-1","chart":"shop","wait":true,"timeout_seconds":120}`,
	))
	if err != nil {
		t.Fatalf("preview error = %v", err)
	}
	if writer.calls[0].wait || writer.calls[0].timeoutSeconds != 0 {
		t.Fatalf("the dry run waited: %+v", writer.calls[0])
	}
	previewID := helmPreviewIDFrom(t, preview.Text)
	if _, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolApplyHelmChange, `{"preview_id":"`+previewID+`"}`,
	)); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	submitted := writer.calls[len(writer.calls)-1]
	if !submitted.wait || submitted.timeoutSeconds != 120 {
		t.Fatalf("the submitted change lost its wait: %+v", submitted)
	}
}

func TestWaitLongerThanATurnIsRefused(t *testing.T) {
	t.Parallel()
	catalogue := helmWriteCatalogue(&recordingHelmWriter{report: helmReport()}, helmWriteGrants())
	if _, err := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmRollback,
		`{"namespace":"web","name":"shop","revision":2,"wait":true,"timeout_seconds":3600}`,
	)); err == nil {
		t.Fatal("an unbounded wait was accepted")
	}
}

// One Helm operation per Cluster at a time is Helm's own constraint, and the
// answer says so rather than reading as an unreachable Agent.
func TestABusyClusterIsSaidToBeBusy(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{err: agentconn.ErrHelmRequestExhausted}
	catalogue := helmWriteCatalogue(writer, helmWriteGrants())
	result, _ := catalogue.Invoke(context.Background(), helmWriteInvocation(
		toolPreviewHelmRollback, `{"namespace":"web","name":"shop","revision":2}`,
	))
	if !result.Failed || !strings.Contains(result.Text, "Helm 操作在执行") {
		t.Fatalf("preview = %+v", result)
	}
}

// The approval prompt for a submission shows one opaque string, so that string
// has to say which of the four actions is about to run.
func TestEveryPreviewIdNamesItsAction(t *testing.T) {
	t.Parallel()
	catalogue := helmWriteCatalogue(&recordingHelmWriter{report: helmReport()}, helmWriteGrants())
	for tool, arguments := range map[string]string{
		toolPreviewHelmInstall:   `{"namespace":"web","name":"shop","repository_id":"r","chart":"c"}`,
		toolPreviewHelmUpgrade:   `{"namespace":"web","name":"shop","repository_id":"r","chart":"c"}`,
		toolPreviewHelmRollback:  `{"namespace":"web","name":"shop","revision":2}`,
		toolPreviewHelmUninstall: `{"namespace":"web","name":"shop"}`,
	} {
		result, err := catalogue.Invoke(
			context.Background(), helmWriteInvocation(tool, arguments))
		if err != nil {
			t.Fatalf("%s error = %v", tool, err)
		}
		action := strings.TrimPrefix(tool, "preview_helm_")
		if !strings.HasPrefix(helmPreviewIDFrom(t, result.Text), "helm_"+action+"_") {
			t.Fatalf("%s produced %q", tool, result.Text)
		}
	}
}

// helmPreviewIDFrom reads the snapshot id out of the answer the model sees,
// which is the only place the executing tool can get it from either.
func helmPreviewIDFrom(t *testing.T, text string) string {
	t.Helper()
	start := strings.Index(text, "{")
	if start < 0 {
		t.Fatalf("no JSON body in %q", text)
	}
	var digest struct {
		PreviewID string `json:"preview_id"`
	}
	if err := json.Unmarshal([]byte(text[start:]), &digest); err != nil {
		t.Fatalf("json.Unmarshal() error = %v on %q", err, text[start:])
	}
	if digest.PreviewID == "" {
		t.Fatalf("no preview_id in %q", text)
	}
	return digest.PreviewID
}
