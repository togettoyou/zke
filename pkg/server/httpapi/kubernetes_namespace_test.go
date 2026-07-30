package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/store"
)

type fakeKubernetesNamespaceService struct {
	listInput   kubernetesresource.ListNamespacesInput
	createInput kubernetesresource.CreateNamespaceInput
	deleteInput kubernetesresource.DeleteNamespaceInput
}

type recordingNamespaceAuditStore struct {
	audit.Store
	events []store.ClusterAuditEvent
}

func (recording *recordingNamespaceAuditStore) RecordClusterEvent(
	_ context.Context,
	event store.ClusterAuditEvent,
) error {
	recording.events = append(recording.events, event)
	return nil
}

func (service *fakeKubernetesNamespaceService) ListNamespaces(
	_ context.Context,
	input kubernetesresource.ListNamespacesInput,
) (kubernetesresource.NamespacePage, error) {
	service.listInput = input
	return kubernetesresource.NamespacePage{
		Namespaces: []kubernetesresource.NamespaceSummary{{
			Name:   "model-serving",
			UID:    "namespace-uid",
			Phase:  "Active",
			Labels: map[string]string{},
		}},
	}, nil
}

func (*fakeKubernetesNamespaceService) GetNamespace(
	context.Context,
	string,
	string,
) (kubernetesresource.NamespaceDetail, error) {
	return kubernetesresource.NamespaceDetail{
		NamespaceSummary: kubernetesresource.NamespaceSummary{
			Name:   "model-serving",
			UID:    "namespace-uid",
			Phase:  "Active",
			Labels: map[string]string{},
		},
		Annotations: map[string]string{},
		Finalizers:  []string{},
	}, nil
}

func (service *fakeKubernetesNamespaceService) CreateNamespace(
	_ context.Context,
	input kubernetesresource.CreateNamespaceInput,
) (kubernetesresource.NamespaceDetail, error) {
	service.createInput = input
	return kubernetesresource.NamespaceDetail{
		NamespaceSummary: kubernetesresource.NamespaceSummary{
			Name:   input.Name,
			UID:    "namespace-uid",
			Phase:  "Active",
			Labels: input.Labels,
		},
		Annotations: map[string]string{},
		Finalizers:  []string{},
	}, nil
}

func (service *fakeKubernetesNamespaceService) DeleteNamespace(
	_ context.Context,
	input kubernetesresource.DeleteNamespaceInput,
) error {
	service.deleteInput = input
	return nil
}

func TestKubernetesNamespaceHandlersPreserveScopeAndSafetyOptions(t *testing.T) {
	t.Parallel()

	const (
		clusterID = "00000000-0000-4000-8000-000000000003"
		key       = "0123456789abcdef"
	)
	service := &fakeKubernetesNamespaceService{}
	auditStore := &recordingNamespaceAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesNamespaceHandler(
		logger,
		service,
		audit.NewService(auditStore, nil),
		time.Second,
	)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User: auth.User{ID: "00000000-0000-4000-8000-000000000001"},
		})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces", handler.list)
	router.POST("/clusters/:cluster_id/namespaces", handler.create)
	router.DELETE(
		"/clusters/:cluster_id/namespaces/:namespace_name",
		handler.delete,
	)

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/clusters/"+clusterID+"/namespaces?limit=25&continue=next",
			nil,
		),
	)
	if response.Code != http.StatusOK ||
		service.listInput.ClusterID != clusterID ||
		service.listInput.Limit != 25 ||
		service.listInput.ContinueToken != "next" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"namespaces"`)) {
		t.Fatalf(
			"list status=%d input=%+v body=%s",
			response.Code,
			service.listInput,
			response.Body.String(),
		)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/clusters/"+clusterID+"/namespaces",
		bytes.NewBufferString(
			`{"name":"model-serving","labels":{"team":"inference"},"dry_run":true,"confirm":false}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.createInput.ClusterID != clusterID ||
		service.createInput.Name != "model-serving" ||
		!service.createInput.DryRun ||
		service.createInput.Confirm ||
		service.createInput.IdempotencyKey != key {
		t.Fatalf(
			"create status=%d input=%+v body=%s",
			response.Code,
			service.createInput,
			response.Body.String(),
		)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodDelete,
		"/clusters/"+clusterID+"/namespaces/model-serving",
		bytes.NewBufferString(
			`{"dry_run":false,"confirm":true,"uid":"namespace-uid","resource_version":"42"}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.deleteInput.ClusterID != clusterID ||
		service.deleteInput.Name != "model-serving" ||
		service.deleteInput.DryRun ||
		!service.deleteInput.Confirm ||
		service.deleteInput.UID != "namespace-uid" ||
		service.deleteInput.ResourceVersion != "42" {
		t.Fatalf(
			"delete status=%d input=%+v body=%s",
			response.Code,
			service.deleteInput,
			response.Body.String(),
		)
	}
	if len(auditStore.events) != 2 ||
		auditStore.events[0].Action !=
			auditaction.KubernetesResourceCreateDryRun ||
		auditStore.events[0].Result != "succeeded" ||
		auditStore.events[1].Action != auditaction.KubernetesResourceDelete ||
		auditStore.events[1].Result != "succeeded" {
		t.Fatalf("unexpected Namespace audit events: %+v", auditStore.events)
	}
}

func TestKubernetesMutationAuditActionDistinguishesDryRun(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		base string
		want string
	}{
		{
			auditaction.KubernetesResourceCreate,
			auditaction.KubernetesResourceCreateDryRun,
		},
		{
			auditaction.KubernetesResourceUpdate,
			auditaction.KubernetesResourceUpdateDryRun,
		},
		{
			auditaction.KubernetesResourcePatch,
			auditaction.KubernetesResourcePatchDryRun,
		},
		{
			auditaction.KubernetesResourceDelete,
			auditaction.KubernetesResourceDeleteDryRun,
		},
	}
	for _, testCase := range testCases {
		if got := kubernetesMutationAuditAction(testCase.base, true); got != testCase.want {
			t.Errorf("dry-run action for %q = %q, want %q", testCase.base, got, testCase.want)
		}
		if got := kubernetesMutationAuditAction(testCase.base, false); got != testCase.base {
			t.Errorf("real action for %q = %q", testCase.base, got)
		}
	}
}
