package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// The service runs on a goroutine now, so everything the tests read back from
// it is written under a mutex. `calls` is what tells a retry that started one
// operation apart from one that started two.
type fakeHelmReleaseWriteService struct {
	mutex     sync.Mutex
	install   *helm.InstallInput
	upgrade   *helm.UpgradeInput
	rollback  *helm.RollbackInput
	uninstall *helm.UninstallInput
	calls     int
	err       error
}

func (service *fakeHelmReleaseWriteService) Install(
	_ context.Context,
	input helm.InstallInput,
) (helmrelease.Report, error) {
	service.mutex.Lock()
	service.install = &input
	service.mutex.Unlock()
	if input.Progress != nil {
		input.Progress(helm.StageResolvingChart, "resolving chart demo@latest")
	}
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Upgrade(
	_ context.Context,
	input helm.UpgradeInput,
) (helmrelease.Report, error) {
	service.mutex.Lock()
	service.upgrade = &input
	service.mutex.Unlock()
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Rollback(
	_ context.Context,
	input helm.RollbackInput,
) (helmrelease.Report, error) {
	service.mutex.Lock()
	service.rollback = &input
	service.mutex.Unlock()
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) Uninstall(
	_ context.Context,
	input helm.UninstallInput,
) (helmrelease.Report, error) {
	service.mutex.Lock()
	service.uninstall = &input
	service.mutex.Unlock()
	return service.report(input.Name, input.Namespace, input.DryRun)
}

func (service *fakeHelmReleaseWriteService) report(
	name string,
	namespace string,
	dryRun bool,
) (helmrelease.Report, error) {
	service.mutex.Lock()
	service.calls++
	err := service.err
	service.mutex.Unlock()
	if err != nil {
		return helmrelease.Report{}, err
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

func (service *fakeHelmReleaseWriteService) installed() *helm.InstallInput {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.install
}

func (service *fakeHelmReleaseWriteService) performed() int {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.calls
}

// stubClusterAuthorizer answers the one question the handler asks for itself.
type stubClusterAuthorizer struct {
	mutex     sync.Mutex
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
	stub.mutex.Lock()
	stub.asked = append(stub.asked, permission)
	stub.clusterID = clusterID
	granted := stub.granted[permission]
	stub.mutex.Unlock()
	if granted {
		return rbac.ResolvedScope{}, nil
	}
	return rbac.ResolvedScope{}, rbac.ErrDenied
}

const (
	helmWriteClusterID = "00000000-0000-4000-8000-000000000009"
	helmWriteBase      = "/clusters/:cluster_id/namespaces/:namespace_name/helm-releases"
	helmWriteURL       = "/clusters/" + helmWriteClusterID + "/namespaces/shop/helm-releases"
	helmOperationBase  = "/clusters/:cluster_id/namespaces/:namespace_name/helm-operations"
	helmOperationURL   = "/clusters/" + helmWriteClusterID + "/namespaces/shop/helm-operations"
	helmWriteActorID   = "00000000-0000-4000-8000-000000000001"
)

// helmWriteHarness is the router plus the two things a test asserts against:
// what was audited, and the account of what ran.
type helmWriteHarness struct {
	router *gin.Engine
	audit  *recordingPodAuditStore
	actor  string
}

func newHelmWriteRouter(
	t *testing.T,
	service helmReleaseWriteService,
	authorizer clusterAuthorizer,
) (*gin.Engine, *recordingPodAuditStore) {
	t.Helper()
	harness := newHelmWriteHarness(t, service, authorizer)
	return harness.router, harness.audit
}

func newHelmWriteHarness(
	t *testing.T,
	service helmReleaseWriteService,
	authorizer clusterAuthorizer,
) *helmWriteHarness {
	t.Helper()
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	operations := helm.NewOperations()
	handler := newHelmReleaseWriteHandler(
		logger,
		service,
		authorizer,
		audit.NewService(auditStore, nil),
		time.Second,
		operations,
	)
	reader := newHelmOperationHandler(logger, operations)
	harness := &helmWriteHarness{
		router: gin.New(),
		audit:  auditStore,
		actor:  helmWriteActorID,
	}
	harness.router.Use(httpmiddleware.RequestLogger(logger))
	harness.router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User: auth.User{ID: harness.actor, Username: "alice"},
		})
		c.Next()
	})
	harness.router.POST(helmWriteBase, handler.install)
	harness.router.PUT(helmWriteBase+"/:release_name", handler.upgrade)
	harness.router.POST(helmWriteBase+"/:release_name/rollback", handler.rollback)
	harness.router.DELETE(helmWriteBase+"/:release_name", handler.uninstall)
	harness.router.GET(helmOperationBase, reader.list)
	harness.router.GET(helmOperationBase+"/:operation_id", reader.get)
	return harness
}

func helmWriteRequest(method string, url string, body string) *http.Request {
	request := httptest.NewRequest(method, url, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// startedOperation reads the identity out of a 202 and fails the test if the
// request was not accepted.
func startedOperation(t *testing.T, response *httptest.ResponseRecorder) helm.Operation {
	t.Helper()
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	return decodeHelmOperation(t, response)
}

func decodeHelmOperation(t *testing.T, response *httptest.ResponseRecorder) helm.Operation {
	t.Helper()
	var body struct {
		Data struct {
			Operation helm.Operation `json:"operation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode operation: %v (body=%s)", err, response.Body.String())
	}
	return body.Data.Operation
}

// awaitHelmOperation polls the read route the way the Console does, until the
// operation stops running. Everything a release change reports about itself
// arrives this way now, so the tests read it the same way.
func awaitHelmOperation(
	t *testing.T,
	harness *helmWriteHarness,
	identifier string,
) helm.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := httptest.NewRecorder()
		harness.router.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet,
			helmOperationURL+"/"+identifier,
			nil,
		))
		if response.Code != http.StatusOK {
			t.Fatalf("read operation status=%d body=%s", response.Code, response.Body.String())
		}
		operation := decodeHelmOperation(t, response)
		if operation.Finished() {
			return operation
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s never finished", identifier)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runHelmWrite performs one request and waits for the operation it started.
func runHelmWrite(
	t *testing.T,
	harness *helmWriteHarness,
	method string,
	url string,
	body string,
) helm.Operation {
	t.Helper()
	response := httptest.NewRecorder()
	harness.router.ServeHTTP(response, helmWriteRequest(method, url, body))
	return awaitHelmOperation(t, harness, startedOperation(t, response).ID)
}

// A dry run writes nothing and needs no confirmation; a real change needs one,
// exactly as every other Cluster write on this Server does. Both are recorded,
// under actions that tell them apart — and both are recorded when the operation
// they started finishes, not when it was accepted.
func TestHelmInstallRequiresConfirmationAndAuditsDryRunsApart(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})

	response := httptest.NewRecorder()
	harness.router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo"}`))
	if response.Code != http.StatusBadRequest ||
		!bytes.Contains(response.Body.Bytes(), []byte("confirmation_required")) {
		t.Fatalf("unconfirmed install status=%d body=%s", response.Code, response.Body.String())
	}
	if len(harness.audit.events) != 1 ||
		harness.audit.events[0].Action != auditaction.KubernetesHelmReleaseInstall ||
		harness.audit.events[0].Result != "failed" {
		t.Fatalf("unconfirmed install audit = %+v", harness.audit.events)
	}

	operation := runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","dry_run":true}`)
	if operation.Status != helm.OperationSucceeded || !operation.DryRun {
		t.Fatalf("dry run operation = %+v", operation)
	}
	if operation.Report == nil || operation.Report.Manifest == "" {
		t.Fatalf("dry run report = %+v", operation.Report)
	}
	// The progress the service reported is part of the account, in order.
	if len(operation.Events) == 0 ||
		!strings.Contains(operation.Events[0].Message, "resolving chart") {
		t.Fatalf("dry run events = %+v", operation.Events)
	}
	if len(harness.audit.events) != 2 ||
		harness.audit.events[1].Action != auditaction.KubernetesHelmReleaseInstallDryRun ||
		harness.audit.events[1].Result != "succeeded" {
		t.Fatalf("dry run audit = %+v", harness.audit.events)
	}

	operation = runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`)
	if operation.Status != helm.OperationSucceeded || operation.DryRun {
		t.Fatalf("install operation = %+v", operation)
	}
	if service.installed() == nil || service.installed().Name != "checkout" ||
		service.installed().Namespace != "shop" ||
		service.installed().ClusterID != helmWriteClusterID {
		t.Fatalf("install input = %+v", service.installed())
	}
	if len(harness.audit.events) != 3 ||
		harness.audit.events[2].Action != auditaction.KubernetesHelmReleaseInstall ||
		harness.audit.events[2].Result != "succeeded" {
		t.Fatalf("install audit = %+v", harness.audit.events)
	}
	// The audit target names the Secret family the release lives in, so an
	// auditor asking "who changed Secrets in this Namespace" finds these too.
	if !strings.Contains(harness.audit.events[2].TargetName, "helm_release:checkout") {
		t.Fatalf("install audit target = %q", harness.audit.events[2].TargetName)
	}
}

// The reply is an operation, not a release: at the moment it is written there
// is no release yet, and saying there is one is exactly the lie that used to
// come back as a timeout while the Cluster went on installing.
func TestHelmInstallAnswersWithAnOperationBeforeItFinishes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	service := &blockingHelmService{release: release}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})

	response := httptest.NewRecorder()
	harness.router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	accepted := startedOperation(t, response)
	if accepted.Status != helm.OperationRunning || accepted.Report != nil {
		t.Fatalf("accepted operation = %+v", accepted)
	}

	// It is readable, and still running, while the Cluster has not answered.
	read := httptest.NewRecorder()
	harness.router.ServeHTTP(read, httptest.NewRequest(
		http.MethodGet, helmOperationURL+"/"+accepted.ID, nil))
	if read.Code != http.StatusOK || decodeHelmOperation(t, read).Finished() {
		t.Fatalf("in-flight read status=%d body=%s", read.Code, read.Body.String())
	}
	// And nothing has been audited yet: the change has not happened.
	if len(harness.audit.events) != 0 {
		t.Fatalf("audit before the operation finished = %+v", harness.audit.events)
	}

	close(release)
	finished := awaitHelmOperation(t, harness, accepted.ID)
	if finished.Status != helm.OperationSucceeded {
		t.Fatalf("finished operation = %+v", finished)
	}
	if len(harness.audit.events) != 1 || harness.audit.events[0].Result != "succeeded" {
		t.Fatalf("audit = %+v", harness.audit.events)
	}
}

// blockingHelmService holds an install open until it is released, which is what
// a Cluster waiting for a rollout looks like from here.
type blockingHelmService struct {
	fakeHelmReleaseWriteService
	release chan struct{}
}

func (service *blockingHelmService) Install(
	ctx context.Context,
	input helm.InstallInput,
) (helmrelease.Report, error) {
	select {
	case <-service.release:
	case <-ctx.Done():
		return helmrelease.Report{}, ctx.Err()
	}
	return service.fakeHelmReleaseWriteService.Install(ctx, input)
}

// A retried submission is a retry, not a second install. The key is what says
// so, and the answer to the second request is the account of the first.
func TestHelmInstallRetriedUnderOneKeyStartsOneOperation(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})
	body := `{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f",` +
		`"chart":"demo","confirm":true}`

	first := httptest.NewRecorder()
	request := helmWriteRequest(http.MethodPost, helmWriteURL, body)
	request.Header.Set("Idempotency-Key", "console-submission-0001")
	harness.router.ServeHTTP(first, request)
	started := startedOperation(t, first)
	awaitHelmOperation(t, harness, started.ID)

	second := httptest.NewRecorder()
	request = helmWriteRequest(http.MethodPost, helmWriteURL, body)
	request.Header.Set("Idempotency-Key", "console-submission-0001")
	harness.router.ServeHTTP(second, request)
	if repeated := startedOperation(t, second); repeated.ID != started.ID {
		t.Fatalf("retry started a second operation: %q then %q", started.ID, repeated.ID)
	}
	if service.performed() != 1 {
		t.Fatalf("the service ran %d times under one key", service.performed())
	}

	// The same key for a different request is a mistake, not a retry: answering
	// with the first operation's account would describe something else.
	third := httptest.NewRecorder()
	request = helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"payments","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f",`+
			`"chart":"demo","confirm":true}`)
	request.Header.Set("Idempotency-Key", "console-submission-0001")
	harness.router.ServeHTTP(third, request)
	if third.Code != http.StatusConflict ||
		!bytes.Contains(third.Body.Bytes(), []byte("idempotency_conflict")) {
		t.Fatalf("status=%d body=%s", third.Code, third.Body.String())
	}
}

// An operation is readable by the operator who started it and by nobody else:
// its report carries a rendered manifest, which is the same content the request
// that started it already returned to that operator and to no one else.
func TestHelmOperationIsReadableOnlyByItsOperator(t *testing.T) {
	t.Parallel()

	harness := newHelmWriteHarness(t, &fakeHelmReleaseWriteService{}, &stubClusterAuthorizer{})
	response := httptest.NewRecorder()
	harness.router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`))
	started := startedOperation(t, response)
	awaitHelmOperation(t, harness, started.ID)

	listed := httptest.NewRecorder()
	harness.router.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, helmOperationURL, nil))
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(started.ID)) {
		t.Fatalf("listing status=%d body=%s", listed.Code, listed.Body.String())
	}
	// A listing is a way back to an operation, not a way to read one: it carries
	// no manifest.
	if bytes.Contains(listed.Body.Bytes(), []byte("kind: Deployment")) {
		t.Fatalf("the listing carried a rendered manifest: %s", listed.Body.String())
	}

	harness.actor = "00000000-0000-4000-8000-00000000000b"
	refused := httptest.NewRecorder()
	harness.router.ServeHTTP(refused, httptest.NewRequest(
		http.MethodGet, helmOperationURL+"/"+started.ID, nil))
	if refused.Code != http.StatusNotFound {
		t.Fatalf("another operator read the operation: status=%d body=%s",
			refused.Code, refused.Body.String())
	}
	empty := httptest.NewRecorder()
	harness.router.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, helmOperationURL, nil))
	if bytes.Contains(empty.Body.Bytes(), []byte(started.ID)) {
		t.Fatalf("another operator listed the operation: %s", empty.Body.String())
	}
}

// Whether a chart may create objects no Namespace contains is the handler's
// decision from `cluster.manage`, never the request body's. A body that tries
// to say so is refused outright rather than quietly ignored.
func TestHelmInstallResolvesClusterScopedFromPermissionsOnly(t *testing.T) {
	t.Parallel()

	denied := &stubClusterAuthorizer{}
	service := &fakeHelmReleaseWriteService{}
	harness := newHelmWriteHarness(t, service, denied)
	response := httptest.NewRecorder()
	harness.router.ServeHTTP(response, helmWriteRequest(http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo",`+
			`"confirm":true,"allow_cluster_scoped":true}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("a body claiming cluster-scoped authorization was accepted: status=%d body=%s",
			response.Code, response.Body.String())
	}

	runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`)
	if service.installed().AllowClusterScoped {
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
	harness = newHelmWriteHarness(t, service, granted)
	runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`)
	if !service.installed().AllowClusterScoped {
		t.Fatal("an operator holding cluster.manage was refused cluster-scoped objects")
	}
}

// Who asked for the change is recorded on the revision itself, so `helm history`
// outside ZKE shows it too.
func TestHelmWriteRecordsTheActorOnTheRevision(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})
	runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f",`+
			`"chart":"demo","confirm":true,"description":"quarterly release"}`)
	if service.installed().Description != "alice: quarterly release" {
		t.Fatalf("description = %q", service.installed().Description)
	}
}

func TestHelmUpgradeRollbackAndUninstallCarryTheirSwitches(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})

	runHelmWrite(t, harness, http.MethodPut, helmWriteURL+"/checkout",
		`{"repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","version":"1.3.0",`+
			`"values":"replicaCount: 2\n","reuse_values":true,"confirm":true}`)
	if service.upgrade == nil || service.upgrade.Name != "checkout" ||
		!service.upgrade.ReuseValues || service.upgrade.Version != "1.3.0" {
		t.Fatalf("upgrade input = %+v", service.upgrade)
	}

	runHelmWrite(t, harness, http.MethodPost, helmWriteURL+"/checkout/rollback",
		`{"revision":4,"confirm":true}`)
	if service.rollback == nil || service.rollback.Revision != 4 {
		t.Fatalf("rollback input = %+v", service.rollback)
	}

	runHelmWrite(t, harness, http.MethodDelete, helmWriteURL+"/checkout",
		`{"keep_history":true,"confirm":true}`)
	if service.uninstall == nil || !service.uninstall.KeepHistory {
		t.Fatalf("uninstall input = %+v", service.uninstall)
	}

	actions := []string{
		auditaction.KubernetesHelmReleaseUpgrade,
		auditaction.KubernetesHelmReleaseRollback,
		auditaction.KubernetesHelmReleaseUninstall,
	}
	if len(harness.audit.events) != len(actions) {
		t.Fatalf("audit events = %+v", harness.audit.events)
	}
	for index, action := range actions {
		if harness.audit.events[index].Action != action ||
			harness.audit.events[index].Result != "succeeded" {
			t.Fatalf("audit event %d = %+v, want %s succeeded",
				index, harness.audit.events[index], action)
		}
	}
}

// A refusal from the Cluster keeps the Cluster's own words: the operator needs
// to read which rule they hit, not that "the operation failed". It arrives on
// the operation now, in the same code the synchronous API used to return.
func TestHelmWriteSurfacesTheClustersRefusal(t *testing.T) {
	t.Parallel()

	service := &fakeHelmReleaseWriteService{
		err: &helm.ReleaseRejection{
			Reason:               "HelmChartCrossNamespace",
			Message:              `chart renders ConfigMap/stolen into Namespace "kube-system"`,
			KubernetesStatusCode: http.StatusForbidden,
		},
	}
	harness := newHelmWriteHarness(t, service, &stubClusterAuthorizer{})
	operation := runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
		`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`)
	if operation.Status != helm.OperationFailed || operation.Failure == nil {
		t.Fatalf("operation = %+v", operation)
	}
	if operation.Failure.Code != "helm_chart_cross_namespace" ||
		!strings.Contains(operation.Failure.Message, "kube-system") {
		t.Fatalf("failure = %+v", operation.Failure)
	}
	if len(harness.audit.events) != 1 || harness.audit.events[0].Result != "failed" {
		t.Fatalf("audit = %+v", harness.audit.events)
	}
}

// An Agent too old to run Helm is not the same as an Agent that is offline:
// only one of them is fixed by waiting.
func TestHelmWriteSeparatesUnsupportedFromOffline(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		err  error
		code string
	}{
		"agent too old":             {agentconn.ErrHelmCapabilityMissing, "helm_unsupported"},
		"agent offline":             {agentconn.ErrAgentNotConnected, "agent_unavailable"},
		"another operation running": {agentconn.ErrHelmRequestExhausted, "helm_busy"},
		"chart is not there":        {helm.ErrChartNotFound, "chart_not_found"},
	} {
		harness := newHelmWriteHarness(
			t,
			&fakeHelmReleaseWriteService{err: testCase.err},
			&stubClusterAuthorizer{},
		)
		operation := runHelmWrite(t, harness, http.MethodPost, helmWriteURL,
			`{"name":"checkout","repository_id":"3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f","chart":"demo","confirm":true}`)
		if operation.Failure == nil || operation.Failure.Code != testCase.code {
			t.Errorf("%s: failure = %+v", name, operation.Failure)
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
