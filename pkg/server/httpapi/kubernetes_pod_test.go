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
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/store"
)

type fakeKubernetesPodService struct {
	listInput       kubernetesresource.ListPodsInput
	getClusterID    string
	getNamespace    string
	getName         string
	deleteInput     kubernetesresource.DeletePodInput
	deleteCallCount int
	evictInput      kubernetesresource.EvictPodInput
	evictCallCount  int
	evictErr        error
}

type recordingPodAuditStore struct {
	audit.Store
	events []store.ClusterAuditEvent
}

func (recording *recordingPodAuditStore) RecordClusterEvent(
	_ context.Context,
	event store.ClusterAuditEvent,
) error {
	recording.events = append(recording.events, event)
	return nil
}

func (service *fakeKubernetesPodService) ListPods(
	_ context.Context,
	input kubernetesresource.ListPodsInput,
) (kubernetesresource.PodPage, error) {
	service.listInput = input
	return kubernetesresource.PodPage{
		Pods: []kubernetesresource.PodSummary{{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  input.Namespace,
			Name:       "inference-abcde",
			UID:        "pod-uid",
			Labels:     map[string]string{},
			Images:     []string{"example/model:v2"},
		}},
	}, nil
}

func (service *fakeKubernetesPodService) GetPod(
	_ context.Context,
	clusterID string,
	namespace string,
	name string,
) (kubernetesresource.PodDetail, error) {
	service.getClusterID = clusterID
	service.getNamespace = namespace
	service.getName = name
	return kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  namespace,
			Name:       name,
			UID:        "pod-uid",
			Labels:     map[string]string{},
			Images:     []string{},
		},
		Annotations:         map[string]string{},
		OwnerReferences:     []kubernetesresource.PodOwnerReference{},
		HostIPs:             []string{},
		PodIPs:              []string{},
		Containers:          []kubernetesresource.PodContainer{},
		InitContainers:      []kubernetesresource.PodContainer{},
		EphemeralContainers: []kubernetesresource.PodContainer{},
		Conditions:          []kubernetesresource.PodCondition{},
	}, nil
}

func (service *fakeKubernetesPodService) DeletePod(
	_ context.Context,
	input kubernetesresource.DeletePodInput,
) error {
	service.deleteCallCount++
	service.deleteInput = input
	return nil
}

func (service *fakeKubernetesPodService) EvictPod(
	_ context.Context,
	input kubernetesresource.EvictPodInput,
) (kubernetesresource.EvictPodResult, error) {
	service.evictCallCount++
	service.evictInput = input
	if service.evictErr != nil {
		return kubernetesresource.EvictPodResult{}, service.evictErr
	}
	return kubernetesresource.EvictPodResult{
		Namespace: input.Namespace,
		Name:      input.Name,
		UID:       input.UID,
		DryRun:    input.DryRun,
		Evicted:   !input.DryRun,
	}, nil
}

func TestKubernetesPodHandlersPreserveScopePaginationAndDeleteSafety(t *testing.T) {
	t.Parallel()

	const (
		clusterID = "00000000-0000-4000-8000-000000000003"
		key       = "0123456789abcdef"
	)
	service := &fakeKubernetesPodService{}
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesPodHandler(
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
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods", handler.list)
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name", handler.get)
	router.DELETE("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name", handler.delete)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/"+clusterID+"/namespaces/model-serving/pods?limit=25&continue=current&label_selector=app%3Dinference&field_selector=spec.nodeName%3Dworker-a",
		nil,
	))
	if response.Code != http.StatusOK ||
		service.listInput.ClusterID != clusterID ||
		service.listInput.Namespace != "model-serving" ||
		service.listInput.Limit != 25 ||
		service.listInput.ContinueToken != "current" ||
		service.listInput.LabelSelector != "app=inference" ||
		service.listInput.FieldSelector != "spec.nodeName=worker-a" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"pods"`)) {
		t.Fatalf("list status=%d input=%+v body=%s", response.Code, service.listInput, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/"+clusterID+"/namespaces/model-serving/pods/inference-abcde",
		nil,
	))
	if response.Code != http.StatusOK ||
		service.getClusterID != clusterID ||
		service.getNamespace != "model-serving" ||
		service.getName != "inference-abcde" {
		t.Fatalf("get status=%d scope=%q/%q/%q body=%s", response.Code, service.getClusterID, service.getNamespace, service.getName, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodDelete,
		"/clusters/"+clusterID+"/namespaces/model-serving/pods/inference-abcde",
		bytes.NewBufferString(`{"dry_run":true,"confirm":false,"uid":"pod-uid","resource_version":"42","grace_period_seconds":15,"propagation_policy":"foreground"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.deleteCallCount != 1 ||
		service.deleteInput.ClusterID != clusterID ||
		service.deleteInput.Namespace != "model-serving" ||
		service.deleteInput.Name != "inference-abcde" ||
		service.deleteInput.UID != "pod-uid" ||
		service.deleteInput.ResourceVersion != "42" ||
		service.deleteInput.GracePeriodSeconds == nil ||
		*service.deleteInput.GracePeriodSeconds != 15 ||
		service.deleteInput.Propagation != agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND ||
		!service.deleteInput.DryRun ||
		service.deleteInput.IdempotencyKey != key {
		t.Fatalf("delete status=%d input=%+v body=%s", response.Code, service.deleteInput, response.Body.String())
	}
	if len(auditStore.events) != 1 ||
		auditStore.events[0].Action != auditaction.KubernetesResourceDeleteDryRun ||
		auditStore.events[0].Result != "succeeded" {
		t.Fatalf("unexpected Pod audit events: %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodDelete,
		"/clusters/"+clusterID+"/namespaces/model-serving/pods/inference-abcde",
		bytes.NewBufferString(`{"dry_run":false,"confirm":true,"uid":"pod-uid","resource_version":"42"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key+"-apply")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.deleteCallCount != 2 ||
		service.deleteInput.DryRun ||
		!service.deleteInput.Confirm ||
		service.deleteInput.IdempotencyKey != key+"-apply" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"deleted":true`)) {
		t.Fatalf("actual delete status=%d input=%+v body=%s", response.Code, service.deleteInput, response.Body.String())
	}
	if len(auditStore.events) != 2 ||
		auditStore.events[1].Action != auditaction.KubernetesResourceDelete ||
		auditStore.events[1].Result != "succeeded" {
		t.Fatalf("unexpected actual Pod delete audit event: %+v", auditStore.events)
	}
}

func TestKubernetesPodHandlersRejectUnsafeRequests(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPodService{}
	handler := newKubernetesPodHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods", handler.list)
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name", handler.get)
	router.DELETE("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name", handler.delete)
	baseURL := "/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods"

	for _, testCase := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "unknown list query", method: http.MethodGet, path: baseURL + "?watch=true"},
		{name: "detail query", method: http.MethodGet, path: baseURL + "/inference-abcde?watch=true"},
		{name: "delete query", method: http.MethodDelete, path: baseURL + "/inference-abcde?force=true", body: `{"dry_run":true,"uid":"pod-uid"}`},
		{name: "confirmation required", method: http.MethodDelete, path: baseURL + "/inference-abcde", body: `{"dry_run":false,"confirm":false,"uid":"pod-uid"}`},
		{name: "UID required", method: http.MethodDelete, path: baseURL + "/inference-abcde", body: `{"dry_run":true,"confirm":false,"uid":""}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewBufferString(testCase.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", testCase.name, response.Code, response.Body.String())
		}
	}
	if service.deleteCallCount != 0 {
		t.Fatal("unsafe Pod deletion reached service")
	}
}

// Eviction has to stay distinguishable from deletion in three places at once:
// the request it sends, the record it leaves, and what an operator is told when
// a PodDisruptionBudget refuses. A blocked eviction that came back as a generic
// conflict would be answered by reloading the page, which never helps.
func TestKubernetesPodEvictionRecordsItsOwnActionAndReportsBudgetRefusals(t *testing.T) {
	t.Parallel()

	const (
		clusterID = "00000000-0000-4000-8000-000000000003"
		key       = "0123456789abcdef"
	)
	service := &fakeKubernetesPodService{}
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesPodHandler(
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
	router.POST(
		"/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/eviction",
		handler.evict,
	)
	evictionURL := "/clusters/" + clusterID +
		"/namespaces/model-serving/pods/inference-abcde/eviction"

	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		evictionURL,
		bytes.NewBufferString(`{"dry_run":true,"confirm":false,"uid":"pod-uid","grace_period_seconds":20}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.evictCallCount != 1 ||
		service.evictInput.ClusterID != clusterID ||
		service.evictInput.Namespace != "model-serving" ||
		service.evictInput.Name != "inference-abcde" ||
		service.evictInput.UID != "pod-uid" ||
		service.evictInput.GracePeriodSeconds == nil ||
		*service.evictInput.GracePeriodSeconds != 20 ||
		!service.evictInput.DryRun ||
		service.evictInput.IdempotencyKey != key ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"evicted":false`)) {
		t.Fatalf("preview status=%d input=%+v body=%s", response.Code, service.evictInput, response.Body.String())
	}
	if len(auditStore.events) != 1 ||
		auditStore.events[0].Action != auditaction.KubernetesPodEvictDryRun ||
		auditStore.events[0].Result != "succeeded" {
		t.Fatalf("unexpected eviction preview audit events: %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost,
		evictionURL,
		bytes.NewBufferString(`{"dry_run":false,"confirm":true,"uid":"pod-uid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key+"-apply")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.evictCallCount != 2 ||
		service.evictInput.DryRun ||
		!service.evictInput.Confirm ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"evicted":true`)) {
		t.Fatalf("eviction status=%d input=%+v body=%s", response.Code, service.evictInput, response.Body.String())
	}
	if len(auditStore.events) != 2 ||
		auditStore.events[1].Action != auditaction.KubernetesPodEvict ||
		auditStore.events[1].Result != "succeeded" {
		t.Fatalf("unexpected eviction audit event: %+v", auditStore.events)
	}

	service.evictErr = &kubernetesresource.PodEvictionBlocked{
		Message: "Cannot evict pod as it would violate the pod's disruption budget.",
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost,
		evictionURL,
		bytes.NewBufferString(`{"dry_run":false,"confirm":true,"uid":"pod-uid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, key+"-blocked")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte("pod_disruption_budget_blocked")) ||
		!bytes.Contains(response.Body.Bytes(), []byte("disruption budget")) {
		t.Fatalf("blocked eviction status=%d body=%s", response.Code, response.Body.String())
	}
	if len(auditStore.events) != 3 ||
		auditStore.events[2].Action != auditaction.KubernetesPodEvict ||
		auditStore.events[2].Result != "failed" {
		t.Fatalf("unexpected blocked eviction audit event: %+v", auditStore.events)
	}
}

// An eviction is a Pod leaving its Node, so it takes the same two guards a
// deletion takes: an explicit confirmation, and the UID of the Pod the operator
// was actually looking at.
func TestKubernetesPodEvictionRefusesUnconfirmedAndUnpinnedRequests(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPodService{}
	handler := newKubernetesPodHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	router.POST(
		"/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/eviction",
		handler.evict,
	)
	baseURL := "/clusters/00000000-0000-4000-8000-000000000003" +
		"/namespaces/model-serving/pods/inference-abcde/eviction"

	for _, testCase := range []struct {
		name string
		path string
		body string
	}{
		{name: "query parameters", path: baseURL + "?force=true", body: `{"dry_run":true,"uid":"pod-uid"}`},
		{name: "confirmation required", path: baseURL, body: `{"dry_run":false,"confirm":false,"uid":"pod-uid"}`},
		{name: "UID required", path: baseURL, body: `{"dry_run":true,"confirm":false,"uid":""}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, testCase.path, bytes.NewBufferString(testCase.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", testCase.name, response.Code, response.Body.String())
		}
	}
	if service.evictCallCount != 0 {
		t.Fatal("unsafe Pod eviction reached service")
	}
}
