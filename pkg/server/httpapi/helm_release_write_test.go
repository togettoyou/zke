package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/helm"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

type fakeHelmReleaseWriteService struct {
	install   *helm.InstallInput
	upgrade   *helm.UpgradeInput
	rollback  *helm.RollbackInput
	uninstall *helm.UninstallInput
	err       error
}

func (service *fakeHelmReleaseWriteService) Install(
	_ context.Context,
	input helm.InstallInput,
) (helmrelease.Report, error) {
	service.install = &input
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Upgrade(
	_ context.Context,
	input helm.UpgradeInput,
) (helmrelease.Report, error) {
	service.upgrade = &input
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Rollback(
	_ context.Context,
	input helm.RollbackInput,
) (helmrelease.Report, error) {
	service.rollback = &input
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Uninstall(
	_ context.Context,
	input helm.UninstallInput,
) (helmrelease.Report, error) {
	service.uninstall = &input
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) report(
	name string,
	namespace string,
	dryRun bool,
) (helmrelease.Report, error) {
	if service.err != nil {
		return helmrelease.Report{}, service.err
	}
	return helmrelease.Report{
		Name:      name,
		Namespace: namespace,
		Revision:  2,
		Status:    "deployed",
		DryRun:    dryRun,
		Manifest:  "kind: Deployment\n",
	}, nil
}

// stubClusterAuthorizer answers the one question the handler asks for itself.
type stubClusterAuthorizer struct {
	granted   map[rbac.Permission]bool
	asked     []rbac.Permission
	clusterID string
}

func (stub *stubClusterAuthorizer) AuthorizeCluster(
	_ context.Context,
	_ string,
	permission rbac.Permission,
	clusterID string,
) (rbac.ResolvedScope, error) {
	stub.asked = append(stub.asked, permission)
	stub.clusterID = clusterID
	if stub.granted[permission] {
		return rbac.ResolvedScope{}, nil
	}
	return rbac.ResolvedScope{}, rbac.ErrDenied
}

const (
	helmWriteClusterID = "00000000-0000-4000-8000-000000000009"
	helmWriteBase      = "/clusters/:cluster_id/namespaces/:namespace_name/helm-releases"
	helmWriteURL       = "/clusters/" + helmWriteClusterID + "/namespaces/shop/helm-releases"
)

func newHelmWriteRouter(
	t *testing.T,
	service helmReleaseWriteService,
	authorizer clusterAuthorizer,
) (*gin.Engine, *recordingPodAuditStore) {
	t.Helper()
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newHelmReleaseWriteHandler(
		logger,
		service,
		authorizer,
		audit.NewService(auditStore, nil),
		time.Second,
	)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User: auth.User{
				ID:       "00000000-0000-4000-8000-000000000001",
				Username: "alice",
			},
		})
		c.Next()
	})
	router.POST(helmWriteBase, handler.install)
	router.PUT(helmWriteBase+"/:release_name", handler.upgrade)
	router.POST(helmWriteBase+"/:release_name/rollback", handler.rollback)
	router.DELETE(helmWriteBase+"/:release_name", handler.uninstall)
	return router, auditStore
}

func helmWriteRequest(method string, url string, body string) *http.Request {
	request := httptest.NewRequest(method, url, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// A dry run writes nothing and needs no confirmation; a real change needs one,
// exactly as every other Cluster write on this Server does. Both are recorded,
// under actions that tell them apart.
func TestHelmInstallRequiresConfirmationAndAuditsDryRunsApart(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	router, auditStore := newHelmWriteRouter(t, service, &stubClusterAuthorizer{})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo"}`))
	if response.Code != http.StatusBadRequest ||
		!bytes.Contains(response.Body.Bytes(), []byte("confirmation_required")) {
		t.Fatalf("unconfirmed install status=%d body=%s", response.Code, response.Body.String())
	}
	if len(auditStore.events) != 1 ||
		auditStore.events[0].Action != auditaction.KubernetesHelmReleaseInstall ||
		auditStore.events[0].Result != "failed" {
		t.Fatalf("unconfirmed install audit = %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","dry_run":true}`))
	if response.Code != http.StatusOK {
		t.Fatalf("dry run status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"dry_run":true`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"manifest"`)) {
		t.Fatalf("dry run body=%s", response.Body.String())
	}
	if len(auditStore.events) != 2 ||
		auditStore.events[1].Action != auditaction.KubernetesHelmReleaseInstallDryRun ||
		auditStore.events[1].Result != "succeeded" {
		t.Fatalf("dry run audit = %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("install status=%d body=%s", response.Code, response.Body.String())
	}
	if service.install == nil || service.install.Name != "checkout" ||
		service.install.Namespace != "shop" ||
		service.install.ClusterID != helmWriteClusterID {
		t.Fatalf("install input = %+v", service.install)
	}
	if len(auditStore.events) != 3 ||
		auditStore.events[2].Action != auditaction.KubernetesHelmReleaseInstall ||
		auditStore.events[2].Result != "succeeded" {
		t.Fatalf("install audit = %+v", auditStore.events)
	}
	// The audit target names the Secret family the release lives in, so an
	// auditor asking "who changed Secrets in this Namespace" finds these too.
	if !strings.Contains(auditStore.events[2].TargetName, "helm_release:checkout") {
		t.Fatalf("install audit target = %q", auditStore.events[2].TargetName)
	}
}

// Whether a chart may create objects no Namespace contains is the handler's
// decision from `cluster.manage`, never the request body's. A body that tries
// to say so is refused outright rather than quietly ignored.
func TestHelmInstallResolvesClusterScopedFromPermissionsOnly(t *testing.T) {
	t.Parallel()

	denied := &stubClusterAuthorizer{}
	service := &fakeHelmReleaseWriteService{}
	router, _ := newHelmWriteRouter(t, service, denied)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo",`+
			`"confirm":true,"allow_cluster_scoped":true}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("a body claiming cluster-scoped authorization was accepted: status=%d body=%s",
			response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.install.AllowClusterScoped {
		t.Fatal("an operator without cluster.manage was allowed cluster-scoped objects")
	}
	if len(denied.asked) != 1 || denied.asked[0] != rbac.PermissionClusterManage {
		t.Fatalf("authorizer was asked %v, want cluster.manage", denied.asked)
	}
	if denied.clusterID != helmWriteClusterID {
		t.Fatalf("authorizer was asked about cluster %q", denied.clusterID)
	}

	granted := &stubClusterAuthorizer{
		granted: map[rbac.Permission]bool{rbac.PermissionClusterManage: true},
	}
	service = &fakeHelmReleaseWriteService{}
	router, _ = newHelmWriteRouter(t, service, granted)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !service.install.AllowClusterScoped {
		t.Fatal("an operator holding cluster.manage was refused cluster-scoped objects")
	}
}

// Who asked for the change is recorded on the revision itself, so `helm history`
// outside ZKE shows it too.
func TestHelmWriteRecordsTheActorOnTheRevision(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	router, _ := newHelmWriteRouter(t, service, &stubClusterAuthorizer{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f",`+
			`"chart":"demo","confirm":true,"description":"quarterly release"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.install.Description != "alice: quarterly release" {
		t.Fatalf("description = %q", service.install.Description)
	}
}

func TestHelmUpgradeRollbackAndUninstallCarryTheirSwitches(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	router, auditStore := newHelmWriteRouter(t, service, &stubClusterAuthorizer{})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPut, helmWriteURL+"/checkout",
		`{"repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","version":"1.3.0",`+
			`"values":"replicaCount: 2\n","reuse_values":true,"confirm":true}`))
	if response.Code != http.StatusOK {
		t.Fatalf("upgrade status=%d body=%s", response.Code, response.Body.String())
	}
	if service.upgrade == nil || service.upgrade.Name != "checkout" ||
		!service.upgrade.ReuseValues || service.upgrade.Version != "1.3.0" {
		t.Fatalf("upgrade input = %+v", service.upgrade)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL+"/checkout/rollback",
		`{"revision":4,"confirm":true}`))
	if response.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", response.Code, response.Body.String())
	}
	if service.rollback == nil || service.rollback.Revision != 4 {
		t.Fatalf("rollback input = %+v", service.rollback)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodDelete, helmWriteURL+"/checkout",
		`{"keep_history":true,"confirm":true}`))
	if response.Code != http.StatusOK {
		t.Fatalf("uninstall status=%d body=%s", response.Code, response.Body.String())
	}
	if service.uninstall == nil || !service.uninstall.KeepHistory {
		t.Fatalf("uninstall input = %+v", service.uninstall)
	}

	actions := []string{
		auditaction.KubernetesHelmReleaseUpgrade,
		auditaction.KubernetesHelmReleaseRollback,
		auditaction.KubernetesHelmReleaseUninstall,
	}
	if len(auditStore.events) != len(actions) {
		t.Fatalf("audit events = %+v", auditStore.events)
	}
	for index, action := range actions {
		if auditStore.events[index].Action != action ||
			auditStore.events[index].Result != "succeeded" {
			t.Fatalf("audit event %d = %+v, want %s succeeded",
				index, auditStore.events[index], action)
		}
	}
}

// A refusal from the Cluster keeps the Cluster's own words: the operator needs
// to read which rule they hit, not that "the operation failed".
func TestHelmWriteSurfacesTheClustersRefusal(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{
		err: &helm.ReleaseRejection{
			Reason:               "HelmChartCrossNamespace",
			Message:              `chart renders ConfigMap/stolen into Namespace "kube-system"`,
			KubernetesStatusCode: http.StatusForbidden,
		},
	}
	router, auditStore := newHelmWriteRouter(t, service, &stubClusterAuthorizer{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("helm_chart_cross_namespace")) ||
		!bytes.Contains(response.Body.Bytes(), []byte("kube-system")) {
		t.Fatalf("body=%s", response.Body.String())
	}
	if len(auditStore.events) != 1 || auditStore.events[0].Result != "failed" {
		t.Fatalf("audit = %+v", auditStore.events)
	}
}

// An Agent too old to run Helm is not the same as an Agent that is offline:
// only one of them is fixed by waiting.
func TestHelmWriteSeparatesUnsupportedFromOffline(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"agent too old": {
			agentconn.ErrHelmCapabilityMissing,
			http.StatusFailedDependency,
			"helm_unsupported",
		},
		"agent offline": {
			agentconn.ErrAgentNotConnected,
			http.StatusServiceUnavailable,
			"agent_unavailable",
		},
		"another operation running": {
			agentconn.ErrHelmRequestExhausted,
			http.StatusTooManyRequests,
			"helm_busy",
		},
		"chart is not there": {
			helm.ErrChartNotFound,
			http.StatusNotFound,
			"chart_not_found",
		},
	} {
		router, _ := newHelmWriteRouter(
			t,
			&fakeHelmReleaseWriteService{err: testCase.err},
			&stubClusterAuthorizer{},
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
			`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
		if response.Code != testCase.status ||
			!bytes.Contains(response.Body.Bytes(), []byte(testCase.code)) {
			t.Errorf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
}

// Without a Helm service the route reports that state rather than panicking on
// an interface holding a nil pointer.
func TestHelmWriteReportsAnUnconfiguredService(t *testing.T) {
	t.Parallel()

	router, _ := newHelmWriteRouter(t, helmServiceOrNil(nil), &stubClusterAuthorizer{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHelmRejectionCodeIsSnakeCase(t *testing.T) {
	t.Parallel()

	for reason, want := range map[string]string{
		"HelmChartCrossNamespace": "helm_chart_cross_namespace",
		"Forbidden":               "forbidden",
		"":                        "helm_release_rejected",
	} {
		if got := helmRejectionCode(reason); got != want {
			t.Errorf("helmRejectionCode(%q) = %q, want %q", reason, got, want)
		}
	}
}
