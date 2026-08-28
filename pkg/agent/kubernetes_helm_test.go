package agent

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fixedRESTMapper answers scope questions from a table, so the guard's rules can
// be exercised without a Kubernetes API server.
type fixedRESTMapper struct {
	meta.RESTMapper
	clusterScoped map[string]bool
}

func (mapper fixedRESTMapper) RESTMapping(
	groupKind schema.GroupKind,
	_ ...string,
) (*meta.RESTMapping, error) {
	scoped, known := mapper.clusterScoped[groupKind.Kind]
	if !known {
		return nil, errors.New("no matches for kind")
	}
	scope := meta.RESTScopeNamespace
	if scoped {
		scope = meta.RESTScopeRoot
	}
	return &meta.RESTMapping{Scope: scope}, nil
}

func testGuard(allowClusterScoped bool) *helmManifestGuard {
	return &helmManifestGuard{
		namespace:          "shop",
		allowClusterScoped: allowClusterScoped,
		mapper: fixedRESTMapper{clusterScoped: map[string]bool{
			"Deployment":               false,
			"ConfigMap":                false,
			"ClusterRole":              true,
			"CustomResourceDefinition": true,
		}},
	}
}

const manifestInReleaseNamespace = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: shop
`

func TestHelmManifestGuardAllowsTheReleaseNamespace(t *testing.T) {
	t.Parallel()

	guard := testGuard(false)
	rendered := bytes.NewBufferString(manifestInReleaseNamespace)
	result, err := guard.Run(rendered)
	if err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	// The guard inspects and never edits: a manifest that came back changed
	// would mean the Cluster got something other than what the chart rendered.
	if result.String() != manifestInReleaseNamespace {
		t.Fatal("Run() modified the rendered manifest")
	}
	if guard.refusal() != nil {
		t.Fatalf("refusal() = %v, want nil", guard.refusal())
	}
}

// The Server authorized one Namespace. A chart writing into a second one would
// be spending an authorization nobody granted.
func TestHelmManifestGuardRefusesAForeignNamespace(t *testing.T) {
	t.Parallel()

	guard := testGuard(true)
	_, err := guard.Run(bytes.NewBufferString(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: stolen
  namespace: kube-system
`))
	if err == nil {
		t.Fatal("Run() accepted an object in another Namespace")
	}
	refusal := guard.refusal()
	if refusal == nil || refusal.reason != "HelmChartCrossNamespace" {
		t.Fatalf("refusal() = %+v, want HelmChartCrossNamespace", refusal)
	}
	if !strings.Contains(refusal.message, "kube-system") ||
		!strings.Contains(refusal.message, "stolen") {
		t.Fatalf("refusal message does not name the object: %q", refusal.message)
	}
}

func TestHelmManifestGuardRefusesClusterScopedObjectsWithoutAuthorization(t *testing.T) {
	t.Parallel()

	guard := testGuard(false)
	_, err := guard.Run(bytes.NewBufferString(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: shop-reader
`))
	if err == nil {
		t.Fatal("Run() accepted a cluster-scoped object without authorization")
	}
	refusal := guard.refusal()
	if refusal == nil || refusal.reason != "HelmChartClusterScoped" {
		t.Fatalf("refusal() = %+v, want HelmChartClusterScoped", refusal)
	}
	if !strings.Contains(refusal.message, "ClusterRole") {
		t.Fatalf("refusal message does not name the kind: %q", refusal.message)
	}
}

func TestHelmManifestGuardAllowsClusterScopedObjectsWhenAuthorized(t *testing.T) {
	t.Parallel()

	guard := testGuard(true)
	if _, err := guard.Run(bytes.NewBufferString(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
`)); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}

// A kind discovery does not know cannot be a pre-existing cluster-scoped
// resource: it is defined by a CustomResourceDefinition in the same chart, and
// that definition is itself a kind discovery knows. Letting the unmappable
// document through is therefore not a way past the rule.
func TestHelmManifestGuardPassesUnmappableKinds(t *testing.T) {
	t.Parallel()

	guard := testGuard(false)
	if _, err := guard.Run(bytes.NewBufferString(`
apiVersion: example.com/v1
kind: Widget
metadata:
  name: first
`)); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	if guard.refusal() != nil {
		t.Fatalf("refusal() = %v, want nil", guard.refusal())
	}
}

func TestHelmManifestGuardIgnoresNonObjectDocuments(t *testing.T) {
	t.Parallel()

	guard := testGuard(false)
	if _, err := guard.Run(bytes.NewBufferString(`
# a template that rendered to comments alone
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
`)); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}

// The distinction an operator acts on is "the Cluster refused this" against
// "there is no such release" against "the chart is wrong": only the first is
// worth retrying and only the last is theirs to fix.
func TestHelmErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		result agentv1.ResultCode
		status int32
	}{
		{
			"release not found",
			driver.ErrReleaseNotFound,
			agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			http.StatusNotFound,
		},
		{
			"no deployed releases",
			driver.ErrNoDeployedReleases,
			agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			http.StatusNotFound,
		},
		{
			"release exists",
			driver.ErrReleaseExists,
			agentv1.ResultCode_RESULT_CODE_CONFLICT,
			http.StatusConflict,
		},
		{
			"deadline exceeded",
			context.DeadlineExceeded,
			agentv1.ResultCode_RESULT_CODE_TIMEOUT,
			http.StatusGatewayTimeout,
		},
		{
			"canceled",
			context.Canceled,
			agentv1.ResultCode_RESULT_CODE_CANCELED,
			http.StatusRequestTimeout,
		},
		{
			"Kubernetes forbade it",
			apierrors.NewForbidden(
				schema.GroupResource{Resource: "deployments"},
				"api",
				errors.New("not allowed"),
			),
			agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
			http.StatusForbidden,
		},
	}
	for _, testCase := range cases {
		response := helmError(testCase.err)
		if response.GetResult() != testCase.result {
			t.Errorf("%s: result = %v, want %v",
				testCase.name, response.GetResult(), testCase.result)
		}
		if response.GetKubernetesStatusCode() != testCase.status {
			t.Errorf("%s: status = %d, want %d",
				testCase.name, response.GetKubernetesStatusCode(), testCase.status)
		}
		if response.GetReason() == "" || response.GetMessage() == "" {
			t.Errorf("%s: failure carries no reason or message", testCase.name)
		}
	}
}

// Helm concatenates the Kubernetes message for every object it failed on, so a
// failed install of a large chart produces an error far past what the protocol
// carries.
func TestHelmMessageIsBounded(t *testing.T) {
	t.Parallel()

	message := helmMessage(errors.New(strings.Repeat("x", 8192)))
	if len(message) > 2100 {
		t.Fatalf("helmMessage() length = %d, want it bounded", len(message))
	}
	if !strings.HasSuffix(message, "…") {
		t.Fatal("a truncated message does not say that it was truncated")
	}
}

// A wait is what `atomic` is built on, so asking for one without the other must
// still wait rather than silently give up the rollback on failure.
func TestHelmTimeoutHonoursTheStreamCeiling(t *testing.T) {
	t.Parallel()

	config := helmHandlerConfig{MaxTimeout: 60_000_000_000} // 60s
	beyond := helmTimeout(config, &agentv1.HelmRequest{TimeoutSeconds: 600})
	if beyond != config.MaxTimeout {
		t.Fatalf("helmTimeout() = %v, want the Stream ceiling %v", beyond, config.MaxTimeout)
	}
	within := helmTimeout(config, &agentv1.HelmRequest{TimeoutSeconds: 30})
	if within != 30_000_000_000 {
		t.Fatalf("helmTimeout() = %v, want 30s", within)
	}
	unset := helmTimeout(config, &agentv1.HelmRequest{})
	if unset != config.MaxTimeout {
		t.Fatalf("helmTimeout() with no request timeout = %v, want the ceiling", unset)
	}
}

func TestHelmDescriptionIsLabelled(t *testing.T) {
	t.Parallel()

	if got := helmDescription(&agentv1.HelmRequest{Description: "alice: deploy"}); got != "ZKE: alice: deploy" {
		t.Fatalf("helmDescription() = %q", got)
	}
	if got := helmDescription(&agentv1.HelmRequest{}); got != "" {
		t.Fatalf("helmDescription() with no description = %q, want empty", got)
	}
}

// silentHelmProgress stands in for the Stream's own sink. A handler always has
// one, so a test that does not care what was reported still has to pass it.
type silentHelmProgress struct{}

func (silentHelmProgress) Progress(string) {}

// The Agent refuses a malformed request itself rather than trusting that the
// Stream layer already checked: this is the last point before it changes its
// own Cluster.
func TestKubernetesHelmHandlerRefusesInvalidRequests(t *testing.T) {
	t.Parallel()

	handler := newKubernetesHelmHandler(helmHandlerConfig{
		RESTConfig: nil,
	})
	response, body, err := handler(
		context.Background(),
		&agentv1.HelmRequest{Action: agentv1.HelmAction_HELM_ACTION_INSTALL},
		bytes.NewReader(nil),
		bytes.NewReader(nil),
		silentHelmProgress{},
	)
	if err != nil {
		t.Fatalf("handler returned a Stream error rather than a response: %v", err)
	}
	if body != nil {
		t.Fatal("a refusal carries a report")
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatal("handler accepted a request with no Kubernetes client")
	}
}

// The idempotency key stays reserved only for an outcome that may have changed
// the Cluster. A dry run wrote nothing and must release it — the Console
// previews and applies under the same key, and a reserved key would turn the
// apply into a conflict.
func TestHelmOutcomeResultReservesTheKeyOnlyForRealChanges(t *testing.T) {
	t.Parallel()

	report := &helmrelease.Report{Name: "checkout", Revision: 2, Manifest: "kind: Deployment\n"}

	preview := helmOutcomeResult(report, nil, true)
	if preview.applied {
		t.Fatal("a dry run reserved the idempotency key")
	}
	if preview.response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		len(preview.body) == 0 {
		t.Fatalf("dry run result = %+v", preview.response)
	}

	applied := helmOutcomeResult(report, nil, false)
	if !applied.applied {
		t.Fatal("a real change did not reserve the idempotency key")
	}

	// A failure after Helm started may have left objects behind, and this Agent
	// cannot tell that apart from one that wrote nothing — so it keeps the key.
	failed := helmOutcomeResult(nil, helmFailure(
		agentv1.ResultCode_RESULT_CODE_INTERNAL,
		http.StatusInternalServerError,
		"HelmOperationFailed",
		"rendering failed",
	), false)
	if !failed.applied || failed.response.GetResult() != agentv1.ResultCode_RESULT_CODE_INTERNAL {
		t.Fatalf("failed result = %+v applied=%v", failed.response, failed.applied)
	}
	if len(failed.body) != 0 {
		t.Fatal("a failure carries a report body")
	}
}

// An oversized manifest is cut and marked rather than dropped, so a preview
// never reads as the whole change when it is not.
func TestHelmOutcomeResultTruncatesAnOversizedManifest(t *testing.T) {
	t.Parallel()

	report := &helmrelease.Report{
		Name:     "checkout",
		Manifest: strings.Repeat("x", helmrelease.MaxManifestBytes+1024),
	}
	result := helmOutcomeResult(report, nil, true)
	if result.response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("result = %+v", result.response)
	}
	if !report.ManifestTruncated || len(report.Manifest) != helmrelease.MaxManifestBytes {
		t.Fatalf("manifest was not truncated: len=%d truncated=%v",
			len(report.Manifest), report.ManifestTruncated)
	}
}

// A refusal this Agent can prove wrote nothing must release the idempotency
// key: the next attempt under it is normally the corrected one, and a reserved
// key would answer that correction with a conflict instead of running it.
func TestHelmOutcomeResultReleasesTheKeyForProvableRefusals(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{
		"HelmChartCrossNamespace",
		"HelmChartClusterScoped",
		"HelmValuesInvalid",
		"HelmReleaseExists",
	} {
		result := helmOutcomeResult(nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
			http.StatusForbidden,
			reason,
			"refused",
		), false)
		if result.applied {
			t.Errorf("%s reserved the idempotency key", reason)
		}
	}
}
