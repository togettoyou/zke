package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/helm"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

type stubChartReader struct {
	repositories helm.RepositoryPage
	charts       helm.ChartPage
	versions     helm.ChartVersionPage
	detail       helm.ChartDetail
	err          error
}

func (stub *stubChartReader) ListRepositories(
	context.Context,
) (helm.RepositoryPage, error) {
	return stub.repositories, stub.err
}

func (stub *stubChartReader) ListCharts(
	context.Context, string, string, int,
) (helm.ChartPage, error) {
	return stub.charts, stub.err
}

func (stub *stubChartReader) ListChartVersions(
	context.Context, string, string,
) (helm.ChartVersionPage, error) {
	return stub.versions, stub.err
}

func (stub *stubChartReader) GetChart(
	context.Context, string, string, string,
) (helm.ChartDetail, error) {
	return stub.detail, stub.err
}

type stubGlobalPermissions struct{ err error }

func (stub stubGlobalPermissions) AuthorizeGlobal(
	context.Context, string, rbac.Permission,
) error {
	return stub.err
}

func chartCatalogue(reader *stubChartReader, global error) *Catalogue {
	return New(Dependencies{
		Charts: reader, GlobalPermissions: stubGlobalPermissions{err: global},
	}, Config{})
}

// The gap an end-to-end test found: the write tools take a `repository_id`
// that is a platform-assigned identifier, and nothing in a Cluster names it.
// Without these the install and upgrade tools are a door with no handle.
func TestTheChartCatalogueIsReachableFromTheCatalogue(t *testing.T) {
	t.Parallel()
	specs := chartCatalogue(&stubChartReader{}, nil).Specs()
	for _, name := range []string{
		toolListHelmRepositories, toolListHelmCharts,
		toolListHelmChartVersions, toolGetHelmChart,
	} {
		spec, found := findHelmSpec(specs, name)
		if !found {
			t.Fatalf("%s is not in the catalogue", name)
		}
		if spec.Mutating || spec.Sensitive {
			t.Fatalf("%s is a read of platform configuration: %+v", name, spec)
		}
	}
}

// `helm.repository.read` has a global scope floor, so it is resolved at global
// scope inside the call. Declaring it in the spec would have the runtime check
// it against the session's Cluster, which is one scope too narrow — a Project
// binding carrying it grants nothing, and checking there would let it grant.
func TestTheChartCatalogueChecksItsPermissionGlobally(t *testing.T) {
	t.Parallel()
	specs := chartCatalogue(&stubChartReader{}, nil).Specs()
	for _, name := range []string{
		toolListHelmRepositories, toolListHelmCharts,
		toolListHelmChartVersions, toolGetHelmChart,
	} {
		spec, _ := findHelmSpec(specs, name)
		for _, permission := range spec.Permissions {
			if permission == rbac.PermissionHelmRepositoryRead {
				t.Fatalf("%s declares %s where the runtime checks it per Cluster",
					name, permission)
			}
		}
	}

	denied := chartCatalogue(&stubChartReader{}, rbac.ErrDenied)
	result, err := denied.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolListHelmRepositories, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !result.Denied ||
		!strings.Contains(result.Text, string(rbac.PermissionHelmRepositoryRead)) {
		t.Fatalf("Invoke() = %+v", result)
	}
}

// A repository row carries the username it authenticates with, its CA
// certificate and its keyring. None of that helps a model choose a chart.
func TestRepositoryListingReturnsIdentityAndNotCredentials(t *testing.T) {
	t.Parallel()
	reader := &stubChartReader{repositories: helm.RepositoryPage{
		Repositories: []helm.Repository{{
			ID: "f6c1a5f0-1f2e-4a0b-9d3c-0c2f6a5b1d20", Name: "bitnami",
			Description: "平台维护", Enabled: true,
			URL: "https://user:hunter2@charts.example.com", Username: "hunter2-user",
			CACertificatePEM: "-----BEGIN CERTIFICATE-----", PublicKeyring: "keyring-body",
		}},
	}}

	result, err := chartCatalogue(reader, nil).Invoke(
		context.Background(), airuntime.ToolInvocation{
			Name: toolListHelmRepositories, ClusterID: testClusterID, UserID: testUserID,
			Arguments: json.RawMessage(`{}`),
		})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	for _, leaked := range []string{
		"hunter2", "BEGIN CERTIFICATE", "keyring-body", "charts.example.com",
	} {
		if strings.Contains(result.Text, leaked) {
			t.Fatalf("the listing leaked %q:\n%s", leaked, result.Text)
		}
	}
	for _, wanted := range []string{"f6c1a5f0-1f2e-4a0b-9d3c-0c2f6a5b1d20", "bitnami"} {
		if !strings.Contains(result.Text, wanted) {
			t.Fatalf("the listing dropped %q:\n%s", wanted, result.Text)
		}
	}
}

// "This chart is missing" and "this list is from Tuesday" look identical to a
// reader who was not told which one it is.
func TestAStaleChartListingSaysSo(t *testing.T) {
	t.Parallel()
	reader := &stubChartReader{charts: helm.ChartPage{
		Charts: []helm.ChartSummary{{Name: "nginx", Version: "1.2.3"}},
		Total:  400, Stale: true,
	}}

	result, _ := chartCatalogue(reader, nil).Invoke(
		context.Background(), airuntime.ToolInvocation{
			Name: toolListHelmCharts, ClusterID: testClusterID, UserID: testUserID,
			Arguments: json.RawMessage(`{"repository_id":"repo-1"}`),
		})
	if !strings.Contains(result.Text, "本地缓存") || !strings.Contains(result.Text, "400") {
		t.Fatalf("Invoke() = %q", result.Text)
	}
}

// A guessed repository identifier used to arrive as the runtime's generic "the
// Agent may be unreachable", which is wrong twice: no Agent was contacted, and
// the fix is to list the catalogue rather than to retry.
func TestAGuessedRepositoryIsNotReportedAsAnUnreachableAgent(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{store.ErrHelmRepositoryNotFound, helm.ErrInvalidInput} {
		result, err := chartCatalogue(&stubChartReader{err: failure}, nil).Invoke(
			context.Background(), airuntime.ToolInvocation{
				Name: toolListHelmCharts, ClusterID: testClusterID, UserID: testUserID,
				Arguments: json.RawMessage(`{"repository_id":"bitnami"}`),
			})
		if err != nil {
			t.Fatalf("Invoke() error = %v", err)
		}
		if !result.Failed || !strings.Contains(result.Text, "list_helm_repositories") {
			t.Fatalf("%v produced %+v", failure, result)
		}
	}
}

// The same guess through the write path. This is the exact call an end-to-end
// test made, and the answer has to name the fix.
func TestAGuessedRepositoryOnAnInstallSaysWhereIdentifiersComeFrom(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{err: helm.ErrInvalidInput}
	result, err := helmWriteCatalogue(writer, helmWriteGrants()).Invoke(
		context.Background(),
		helmWriteInvocation(toolPreviewHelmInstall,
			`{"namespace":"web","name":"shop","repository_id":"bitnami","chart":"nginx"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !result.Failed || !strings.Contains(result.Text, "list_helm_repositories") {
		t.Fatalf("Invoke() = %+v", result)
	}
}

// A rollback names no chart, so its refusal must not send the model looking for
// a repository it never had to supply.
func TestAnInvalidRollbackDoesNotBlameTheChartRepository(t *testing.T) {
	t.Parallel()
	writer := &recordingHelmWriter{err: helm.ErrInvalidInput}
	result, _ := helmWriteCatalogue(writer, helmWriteGrants()).Invoke(
		context.Background(),
		helmWriteInvocation(toolPreviewHelmRollback,
			`{"namespace":"Web","name":"shop","revision":2}`),
	)
	if strings.Contains(result.Text, "repository_id") {
		t.Fatalf("a rollback refusal mentioned the chart repository: %q", result.Text)
	}
}

// The chart's own values.yaml is where the valid value paths are written down.
// It is chart content, not cluster content — nothing in it came out of a
// Release, so the rule that keeps release values out does not apply to it.
func TestChartDetailReturnsTheChartsOwnDefaultValues(t *testing.T) {
	t.Parallel()
	reader := &stubChartReader{detail: helm.ChartDetail{
		Name: "nginx", Version: "1.2.3", AppVersion: "1.25",
		Values:       "replicaCount: 1\nimage:\n  tag: \"1.25\"\n",
		ValuesSchema: "{}",
	}}

	result, _ := chartCatalogue(reader, nil).Invoke(
		context.Background(), airuntime.ToolInvocation{
			Name: toolGetHelmChart, ClusterID: testClusterID, UserID: testUserID,
			Arguments: json.RawMessage(`{"repository_id":"repo-1","chart":"nginx"}`),
		})
	for _, wanted := range []string{"replicaCount", "values_schema_present", "1.2.3"} {
		if !strings.Contains(result.Text, wanted) {
			t.Fatalf("chart detail dropped %q:\n%s", wanted, result.Text)
		}
	}
}

func TestChartCatalogueToolsAreAbsentWithoutTheirDependencies(t *testing.T) {
	t.Parallel()
	for _, dependencies := range []Dependencies{
		{GlobalPermissions: stubGlobalPermissions{}},
		{Charts: &stubChartReader{}},
	} {
		for _, spec := range New(dependencies, Config{}).Specs() {
			switch spec.Name {
			case toolListHelmRepositories, toolListHelmCharts,
				toolListHelmChartVersions, toolGetHelmChart:
				t.Fatalf("catalogue advertises %s without both dependencies", spec.Name)
			}
		}
	}
}
