package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type stubHelmReader struct {
	page   kubernetesresource.HelmReleasePage
	detail kubernetesresource.HelmReleaseDetail
	err    error
	// namespace and name record what the tool actually asked for, so a test can
	// tell a narrow read from one that quietly widened.
	namespace string
	name      string
	revision  int64
}

func (stub *stubHelmReader) ListHelmReleases(
	_ context.Context, input kubernetesresource.ListHelmReleasesInput,
) (kubernetesresource.HelmReleasePage, error) {
	stub.namespace = input.Namespace
	return stub.page, stub.err
}

func (stub *stubHelmReader) ListHelmReleaseRevisions(
	_ context.Context, _ string, namespace string, name string,
) (kubernetesresource.HelmReleasePage, error) {
	stub.namespace, stub.name = namespace, name
	return stub.page, stub.err
}

func (stub *stubHelmReader) GetHelmRelease(
	_ context.Context, _ string, namespace string, name string, revision int64,
) (kubernetesresource.HelmReleaseDetail, error) {
	stub.namespace, stub.name, stub.revision = namespace, name, revision
	return stub.detail, stub.err
}

func helmCatalogue(reader HelmReleaseReader) *Catalogue {
	return New(Dependencies{Helm: reader}, Config{})
}

func helmInvocation(name, arguments string) airuntime.ToolInvocation {
	return airuntime.ToolInvocation{
		Name: name, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(arguments),
	}
}

// Every Helm tool requires both permissions, always. `cluster.read` is not
// enough because the storage is a Secret, and `cluster.secret.read` is not
// enough because this is still a Cluster read — the same pair the Console's
// own release routes require. A tool that answered to one of them would be a
// cheaper door to the same data.
func TestHelmToolsRequireClusterReadAndSecretRead(t *testing.T) {
	t.Parallel()
	specs := helmCatalogue(&stubHelmReader{}).Specs()
	for _, name := range []string{
		toolListHelmReleases, toolListHelmReleaseRevisions, toolGetHelmRelease,
	} {
		spec, found := findHelmSpec(specs, name)
		if !found {
			t.Fatalf("%s is not in the catalogue", name)
		}
		holds := map[rbac.Permission]bool{}
		for _, permission := range spec.Permissions {
			holds[permission] = true
		}
		if !holds[rbac.PermissionClusterRead] || !holds[rbac.PermissionClusterSecretRead] {
			t.Fatalf("%s permissions = %v", name, spec.Permissions)
		}
		if spec.Mutating {
			t.Fatalf("%s is marked mutating; the Helm tools only read", name)
		}
	}
}

// A deployment that did not compose the reader has no Helm tools at all,
// rather than three tools that fail on every call.
func TestHelmToolsAreAbsentWithoutTheReader(t *testing.T) {
	t.Parallel()
	for _, spec := range New(Dependencies{}, Config{}).Specs() {
		if strings.Contains(spec.Name, "helm") {
			t.Fatalf("catalogue advertises %s without a Helm reader", spec.Name)
		}
	}
}

// Reading one release decodes the payload the Secret holds, which is why it
// stops for a person; the two listings read only the Secret's labels.
func TestOnlyTheReleaseReadIsSensitive(t *testing.T) {
	t.Parallel()
	specs := helmCatalogue(&stubHelmReader{}).Specs()
	for name, want := range map[string]bool{
		toolListHelmReleases:         false,
		toolListHelmReleaseRevisions: false,
		toolGetHelmRelease:           true,
	} {
		spec, _ := findHelmSpec(specs, name)
		if spec.Sensitive != want {
			t.Fatalf("%s sensitive = %t, want %t", name, spec.Sensitive, want)
		}
	}
}

func TestListHelmReleasesReportsWhatIsInstalled(t *testing.T) {
	t.Parallel()
	reader := &stubHelmReader{page: kubernetesresource.HelmReleasePage{
		Releases: []kubernetesresource.HelmRelease{
			{Namespace: "web", Name: "shop", Revision: 4, Status: "deployed", Updated: time.Now()},
		},
	}}

	result, err := helmCatalogue(reader).Invoke(
		context.Background(),
		helmInvocation(toolListHelmReleases, `{"namespace":"web"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if reader.namespace != "web" {
		t.Fatalf("read namespace = %q", reader.namespace)
	}
	if !strings.Contains(result.Text, "shop") || !strings.Contains(result.Text, "deployed") {
		t.Fatalf("Invoke() text = %q", result.Text)
	}
	if len(result.Evidence) != 1 ||
		result.Evidence[0].Kind != aisession.EvidenceHelmRelease ||
		result.Evidence[0].Name != "shop" {
		t.Fatalf("Invoke() evidence = %+v", result.Evidence)
	}
}

// The newest revision is the one the cluster is running, and a rollback target
// has to be one of the others — so the listing has to say which is which.
func TestReleaseHistoryMarksTheCurrentRevision(t *testing.T) {
	t.Parallel()
	reader := &stubHelmReader{page: kubernetesresource.HelmReleasePage{
		Releases: []kubernetesresource.HelmRelease{
			{Namespace: "web", Name: "shop", Revision: 4, Status: "failed"},
			{Namespace: "web", Name: "shop", Revision: 3, Status: "superseded"},
		},
	}}

	result, err := helmCatalogue(reader).Invoke(
		context.Background(),
		helmInvocation(toolListHelmReleaseRevisions, `{"namespace":"web","name":"shop"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	var rows []map[string]any
	decodeRows(t, result.Text, &rows)
	if len(rows) != 2 || rows[0]["current"] != true || rows[1]["current"] != false {
		t.Fatalf("history rows = %+v", rows)
	}
}

// The invariant this whole file exists to keep: a release's values, its
// NOTES.txt and its rendered manifest are Secret content and do not reach the
// model context or the durable trail. What crosses is the shape — which paths
// were overridden — and never what they were set to.
func TestReleaseDetailReturnsValuePathsAndNeverValues(t *testing.T) {
	t.Parallel()
	reader := &stubHelmReader{detail: kubernetesresource.HelmReleaseDetail{
		HelmRelease: kubernetesresource.HelmRelease{
			Namespace: "web", Name: "shop", Revision: 4, Status: "deployed",
		},
		ChartName: "shop", ChartVersion: "1.4.2",
		Notes: "your password is hunter2-in-the-notes",
		Values: map[string]any{
			"image": map[string]any{"tag": "v9-not-a-secret"},
			"auth":  map[string]any{"rootPassword": "hunter2-in-the-values"},
			"extraEnv": []any{
				map[string]any{"name": "TOKEN", "value": "hunter2-in-a-list"},
			},
		},
		Manifest: "apiVersion: apps/v1\nkind: Deployment\n" +
			"metadata:\n  name: shop\n  namespace: web\n" +
			"---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: shop-auth\n  namespace: web\n" +
			"data:\n  password: aHVudGVyMi1pbi10aGUtbWFuaWZlc3Q=\n",
	}}

	result, err := helmCatalogue(reader).Invoke(
		context.Background(),
		helmInvocation(toolGetHelmRelease, `{"namespace":"web","name":"shop"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	for _, leaked := range []string{
		"hunter2-in-the-values", "hunter2-in-a-list", "hunter2-in-the-notes",
		"aHVudGVyMi1pbi10aGUtbWFuaWZlc3Q=",
	} {
		if strings.Contains(result.Text, leaked) {
			t.Fatalf("release detail leaked Secret content %q:\n%s", leaked, result.Text)
		}
	}
	for _, wanted := range []string{
		"auth.rootPassword", "image.tag", "extraEnv[]", "1.4.2",
		"apps/v1 Deployment web/shop", "v1 Secret web/shop-auth",
	} {
		if !strings.Contains(result.Text, wanted) {
			t.Fatalf("release detail dropped %q:\n%s", wanted, result.Text)
		}
	}
}

// A release read cites the objects the chart rendered, because that is where a
// broken rollout is actually diagnosed — and those are ordinary objects, so
// they are cited as resources and rechecked on `cluster.read`.
func TestReleaseDetailCitesTheReleaseAndItsRenderedObjects(t *testing.T) {
	t.Parallel()
	reader := &stubHelmReader{detail: kubernetesresource.HelmReleaseDetail{
		HelmRelease: kubernetesresource.HelmRelease{Namespace: "web", Name: "shop", Revision: 1},
		Manifest: "apiVersion: apps/v1\nkind: Deployment\n" +
			"metadata:\n  name: shop\n  namespace: web\n",
	}}

	result, _ := helmCatalogue(reader).Invoke(
		context.Background(),
		helmInvocation(toolGetHelmRelease, `{"namespace":"web","name":"shop"}`),
	)

	if len(result.Evidence) != 2 {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
	if result.Evidence[0].Kind != aisession.EvidenceHelmRelease {
		t.Fatalf("first citation = %+v", result.Evidence[0])
	}
	if result.Evidence[1].Kind != aisession.EvidenceResource ||
		result.Evidence[1].GVK != "apps/v1/Deployment" {
		t.Fatalf("second citation = %+v", result.Evidence[1])
	}
}

// The manifest arrives already cut at the service's size bound, so its last
// document is routinely half a document — and a cut inside a quoted scalar
// leaves YAML that does not parse at all. Refusing the whole inventory over
// that tail would throw away objects that really are in the release.
func TestRenderedInventorySurvivesATruncatedManifest(t *testing.T) {
	t.Parallel()
	objects, partial := helmManifestObjects(
		"apiVersion: v1\nkind: Service\nmetadata:\n  name: shop\n  namespace: web\n" +
			"---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: \"shop-",
	)
	if len(objects) != 1 || objects[0].name != "shop" {
		t.Fatalf("objects = %+v", objects)
	}
	if !partial {
		t.Fatal("a manifest cut mid-document was reported as complete")
	}
}

// A manifest the service itself cut is partial even when every document it
// still holds parses cleanly: the objects past the cut are simply not there.
func TestRenderedInventoryIsPartialWhenTheServiceCutTheManifest(t *testing.T) {
	t.Parallel()
	digest, _ := helmReleaseDigest(kubernetesresource.HelmReleaseDetail{
		Manifest:          "apiVersion: v1\nkind: Service\nmetadata:\n  name: shop\n  namespace: web\n",
		ManifestTruncated: true,
	})
	if digest["rendered_objects_partial"] != true {
		t.Fatalf("digest = %+v", digest)
	}
}

// "No such release" is not "the Agent may be unreachable". The model is told
// which one it is, because only one of them is fixed by asking again.
func TestAMissingReleaseIsAnAnswerRatherThanAnError(t *testing.T) {
	t.Parallel()
	reader := &stubHelmReader{err: kubernetesresource.ErrHelmReleaseNotFound}

	result, err := helmCatalogue(reader).Invoke(
		context.Background(),
		helmInvocation(toolGetHelmRelease, `{"namespace":"web","name":"gone"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !result.Failed || !strings.Contains(result.Text, "list_helm_releases") {
		t.Fatalf("Invoke() = %+v", result)
	}
}

// The target is what the approval prompt, the trajectory and the audit row
// name before the call runs, so it has to survive argument decoding.
func TestReleaseTargetNamesTheNamespaceAndRelease(t *testing.T) {
	t.Parallel()
	target := helmReleaseTarget(json.RawMessage(`{"namespace":"web","name":"shop"}`))
	if target == nil || target.Namespace != "web" || target.Name != "shop" {
		t.Fatalf("helmReleaseTarget() = %+v", target)
	}
	if helmReleaseTarget(json.RawMessage(`{"cluster":"other"}`)) != nil {
		t.Fatal("helmReleaseTarget() accepted an undeclared field")
	}
}

func findHelmSpec(specs []airuntime.ToolSpec, name string) (airuntime.ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return airuntime.ToolSpec{}, false
}

// The tool answers with a header line and then the JSON body; a test that
// wants the rows parses from the first bracket.
func decodeRows(t *testing.T, text string, target any) {
	t.Helper()
	start := strings.Index(text, "[")
	if start < 0 {
		t.Fatalf("no JSON body in %q", text)
	}
	if err := json.Unmarshal([]byte(text[start:]), target); err != nil {
		t.Fatalf("json.Unmarshal() error = %v on %q", err, text[start:])
	}
}
