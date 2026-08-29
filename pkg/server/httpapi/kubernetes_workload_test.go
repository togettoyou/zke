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
)

type fakeKubernetesWorkloadService struct {
	listInput    kubernetesresource.ListWorkloadsInput
	getClusterID string
	getNamespace string
	getResource  kubernetesresource.WorkloadResource
	getName      string
	createInput  kubernetesresource.CreateWorkloadInput
	cloneInput   kubernetesresource.CloneWorkloadInput
	updateInput  kubernetesresource.UpdateWorkloadInput
	scaleInput   kubernetesresource.ScaleWorkloadInput
	restartInput kubernetesresource.WorkloadMutationInput
	suspendInput kubernetesresource.SetWorkloadSuspensionInput
	suspendCalls []kubernetesresource.SetWorkloadSuspensionInput
	deleteInput  kubernetesresource.DeleteWorkloadInput

	revisionsInput kubernetesresource.ListWorkloadRevisionsInput
	rollbackInput  kubernetesresource.RollbackWorkloadInput
	rollbackError  error

	triggerInput     kubernetesresource.TriggerCronJobInput
	triggerCallCount int
	triggerError     error
}

func (service *fakeKubernetesWorkloadService) ListWorkloads(
	_ context.Context,
	input kubernetesresource.ListWorkloadsInput,
) (kubernetesresource.WorkloadPage, error) {
	service.listInput = input
	return kubernetesresource.WorkloadPage{
		Workloads: []kubernetesresource.WorkloadSummary{{
			Resource:   input.Resource,
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  input.Namespace,
			Name:       "inference",
			Labels:     map[string]string{},
			Status:     "available",
			Images:     []string{"example/model:v2"},
		}},
	}, nil
}

func (service *fakeKubernetesWorkloadService) GetWorkload(
	_ context.Context,
	clusterID string,
	namespace string,
	resource kubernetesresource.WorkloadResource,
	name string,
) (kubernetesresource.WorkloadDetail, error) {
	service.getClusterID = clusterID
	service.getNamespace = namespace
	service.getResource = resource
	service.getName = name
	return kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:   resource,
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  namespace,
			Name:       name,
			Labels:     map[string]string{},
			Status:     "available",
			Images:     []string{"example/model:v2"},
		},
		Annotations:               map[string]string{},
		Containers:                []kubernetesresource.WorkloadContainerTemplate{},
		InitContainers:            []kubernetesresource.WorkloadContainerTemplate{},
		TopologySpreadConstraints: []kubernetesresource.WorkloadTopologySpreadConstraint{},
		Conditions:                []kubernetesresource.WorkloadCondition{},
	}, nil
}

func (service *fakeKubernetesWorkloadService) CreateWorkload(
	_ context.Context,
	input kubernetesresource.CreateWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.createInput = input
	return fakeWorkloadMutationResult(kubernetesresource.WorkloadMutationInput{
		ClusterID:      input.ClusterID,
		Namespace:      input.Namespace,
		Resource:       input.Resource,
		Name:           input.Name,
		DryRun:         input.DryRun,
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	}), nil
}

func (service *fakeKubernetesWorkloadService) CloneWorkload(
	_ context.Context,
	input kubernetesresource.CloneWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.cloneInput = input
	return fakeWorkloadMutationResult(kubernetesresource.WorkloadMutationInput{
		ClusterID:      input.ClusterID,
		Namespace:      input.Namespace,
		Resource:       input.Resource,
		Name:           input.Name,
		DryRun:         input.DryRun,
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	}), nil
}

func (service *fakeKubernetesWorkloadService) UpdateWorkload(
	_ context.Context,
	input kubernetesresource.UpdateWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.updateInput = input
	return fakeWorkloadMutationResult(kubernetesresource.WorkloadMutationInput{
		ClusterID: input.ClusterID,
		Namespace: input.Namespace,
		Resource:  input.Resource,
		Name:      input.Name,
	}), nil
}

func (service *fakeKubernetesWorkloadService) ScaleWorkload(
	_ context.Context,
	input kubernetesresource.ScaleWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.scaleInput = input
	return fakeWorkloadMutationResult(input.WorkloadMutationInput), nil
}

func (service *fakeKubernetesWorkloadService) RestartWorkload(
	_ context.Context,
	input kubernetesresource.WorkloadMutationInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.restartInput = input
	return fakeWorkloadMutationResult(input), nil
}

func (service *fakeKubernetesWorkloadService) SetWorkloadSuspension(
	_ context.Context,
	input kubernetesresource.SetWorkloadSuspensionInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.suspendInput = input
	service.suspendCalls = append(service.suspendCalls, input)
	return fakeWorkloadMutationResult(input.WorkloadMutationInput), nil
}

func (service *fakeKubernetesWorkloadService) TriggerCronJob(
	_ context.Context,
	input kubernetesresource.TriggerCronJobInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.triggerCallCount++
	service.triggerInput = input
	if service.triggerError != nil {
		return kubernetesresource.WorkloadDetail{}, service.triggerError
	}
	return fakeWorkloadMutationResult(kubernetesresource.WorkloadMutationInput{
		ClusterID: input.ClusterID,
		Namespace: input.Namespace,
		Resource:  kubernetesresource.WorkloadJobs,
		Name:      input.Name + "-manual-0a1b2c3d",
		DryRun:    input.DryRun,
		Confirm:   input.Confirm,
	}), nil
}

func (service *fakeKubernetesWorkloadService) ListWorkloadRevisions(
	_ context.Context,
	input kubernetesresource.ListWorkloadRevisionsInput,
) (kubernetesresource.WorkloadRevisionPage, error) {
	service.revisionsInput = input
	return kubernetesresource.WorkloadRevisionPage{
		Revisions: []kubernetesresource.WorkloadRevision{{
			Revision:       3,
			Name:           input.Name + "-7c9f",
			UID:            "00000000-0000-4000-8000-0000000000f1",
			Current:        true,
			Images:         []string{"example/model:v2"},
			Containers:     []kubernetesresource.WorkloadRevisionContainer{},
			InitContainers: []kubernetesresource.WorkloadRevisionContainer{},
		}},
	}, nil
}

func (service *fakeKubernetesWorkloadService) RollbackWorkload(
	_ context.Context,
	input kubernetesresource.RollbackWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	service.rollbackInput = input
	if service.rollbackError != nil {
		return kubernetesresource.WorkloadDetail{}, service.rollbackError
	}
	return fakeWorkloadMutationResult(input.WorkloadMutationInput), nil
}

func (service *fakeKubernetesWorkloadService) DeleteWorkload(
	_ context.Context,
	input kubernetesresource.DeleteWorkloadInput,
) error {
	service.deleteInput = input
	return nil
}

func fakeWorkloadMutationResult(
	input kubernetesresource.WorkloadMutationInput,
) kubernetesresource.WorkloadDetail {
	return kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:  input.Resource,
			Namespace: input.Namespace,
			Name:      input.Name,
			Labels:    map[string]string{},
			Images:    []string{},
		},
		Annotations:               map[string]string{},
		Containers:                []kubernetesresource.WorkloadContainerTemplate{},
		InitContainers:            []kubernetesresource.WorkloadContainerTemplate{},
		TopologySpreadConstraints: []kubernetesresource.WorkloadTopologySpreadConstraint{},
		Conditions:                []kubernetesresource.WorkloadCondition{},
	}
}

func TestKubernetesWorkloadHandlersPreserveClusterNamespaceAndResource(t *testing.T) {
	t.Parallel()

	const clusterID = "00000000-0000-4000-8000-000000000003"
	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handler.list,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handler.get,
	)

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/clusters/"+clusterID+
				"/namespaces/model-serving/workloads/deployments"+
				"?limit=25&continue=next&label_selector=app%3Dinference",
			nil,
		),
	)
	if response.Code != http.StatusOK ||
		service.listInput.ClusterID != clusterID ||
		service.listInput.Namespace != "model-serving" ||
		service.listInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.listInput.Limit != 25 ||
		service.listInput.ContinueToken != "next" ||
		service.listInput.LabelSelector != "app=inference" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"workloads"`)) {
		t.Fatalf(
			"list status=%d input=%+v body=%s",
			response.Code,
			service.listInput,
			response.Body.String(),
		)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/clusters/"+clusterID+
				"/namespaces/model-serving/workloads/deployments/inference",
			nil,
		),
	)
	if response.Code != http.StatusOK ||
		service.getClusterID != clusterID ||
		service.getNamespace != "model-serving" ||
		service.getResource != kubernetesresource.WorkloadDeployments ||
		service.getName != "inference" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"name":"inference"`)) {
		t.Fatalf(
			"get status=%d scope=%q/%q/%q/%q body=%s",
			response.Code,
			service.getClusterID,
			service.getNamespace,
			service.getResource,
			service.getName,
			response.Body.String(),
		)
	}
}

func TestKubernetesWorkloadHandlersRejectUnknownResourceAndDetailQuery(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handler.list,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handler.get,
	)

	for _, path := range []string{
		"/clusters/cluster/namespaces/default/workloads/pods",
		"/clusters/cluster/namespaces/default/workloads/deployments/demo?watch=true",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if service.listInput.ClusterID != "" || service.getClusterID != "" {
		t.Fatal("invalid workload request reached service")
	}
}

func TestKubernetesWorkloadMutationHandlersPreserveSafetyAndScope(t *testing.T) {
	t.Parallel()

	const (
		clusterID      = "00000000-0000-4000-8000-000000000003"
		idempotencyKey = "0123456789abcdef"
	)
	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	collectionRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource"
	baseRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource/:workload_name"
	router.POST(collectionRoute, handler.create)
	router.POST(baseRoute+"/clone", handler.clone)
	router.PUT(baseRoute, handler.update)
	router.POST(baseRoute+"/scale", handler.scale)
	router.POST(baseRoute+"/restart", handler.restart)
	router.POST(baseRoute+"/suspend", handler.suspend)
	router.POST(baseRoute+"/resume", handler.resume)
	router.DELETE(baseRoute, handler.delete)
	baseURL := "/clusters/" + clusterID + "/namespaces/model-serving/workloads"

	testCases := []struct {
		method string
		path   string
		body   string
	}{
		{
			method: http.MethodPost,
			path:   baseURL + "/deployments",
			body: `{
				"name":"api",
				"labels":{"app":"api"},
				"containers":[{
					"name":"main",
					"image":"example/api:v1",
					"image_pull_policy":"IfNotPresent",
					"ports":[{"name":"http","container_port":8080,"protocol":"TCP"}]
				}],
				"affinity":{"node_affinity":{"required":[{"match_expressions":[{"key":"disk","operator":"In","values":["ssd"]}]}]}},
				"topology_spread_constraints":[{"max_skew":1,"topology_key":"topology.kubernetes.io/zone","when_unsatisfiable":"DoNotSchedule"}],
				"replicas":3,
				"dry_run":false,
				"confirm":true
			}`,
		},
		{
			method: http.MethodPost,
			path:   baseURL + "/deployments/inference/scale",
			body:   `{"replicas":4,"dry_run":false,"confirm":true}`,
		},
		{
			method: http.MethodPost,
			path:   baseURL + "/deployments/api/clone",
			body: `{
				"name":"api-copy",
				"source_uid":"deployment-uid",
				"source_resource_version":"12",
				"dry_run":false,
				"confirm":true
			}`,
		},
		{
			method: http.MethodPost,
			path:   baseURL + "/daemonsets/device-plugin/restart",
			body:   `{"dry_run":true,"confirm":false}`,
		},
		{
			method: http.MethodPost,
			path:   baseURL + "/cronjobs/cleanup/suspend",
			body:   `{"dry_run":false,"confirm":true}`,
		},
		{
			method: http.MethodPost,
			path:   baseURL + "/cronjobs/cleanup/resume",
			body:   `{"dry_run":false,"confirm":true}`,
		},
		{
			method: http.MethodPut,
			path:   baseURL + "/deployments/api",
			body: `{
				"uid":"deployment-uid",
				"resource_version":"12",
				"labels":{"app":"api"},
				"containers":[{
					"name":"main",
					"image":"example/api:v2"
				}],
				"replicas":5,
				"dry_run":false,
				"confirm":true
			}`,
		},
		{
			method: http.MethodDelete,
			path:   baseURL + "/jobs/finetune",
			body: `{
				"dry_run":false,
				"confirm":true,
				"uid":"job-uid",
				"resource_version":"42",
				"grace_period_seconds":30,
				"propagation_policy":"foreground"
			}`,
		},
	}
	for _, testCase := range testCases {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			testCase.method,
			testCase.path,
			bytes.NewBufferString(testCase.body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
		router.ServeHTTP(response, request)
		wantStatus := http.StatusOK
		if testCase.path == baseURL+"/deployments" {
			wantStatus = http.StatusCreated
		}
		if testCase.path == baseURL+"/deployments/api/clone" {
			wantStatus = http.StatusCreated
		}
		if response.Code != wantStatus {
			t.Fatalf(
				"%s %s status=%d body=%s",
				testCase.method,
				testCase.path,
				response.Code,
				response.Body,
			)
		}
	}

	if service.createInput.ClusterID != clusterID ||
		service.createInput.Namespace != "model-serving" ||
		service.createInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.createInput.Name != "api" ||
		service.createInput.Labels["app"] != "api" ||
		len(service.createInput.Containers) != 1 ||
		service.createInput.Containers[0].Image != "example/api:v1" ||
		len(service.createInput.Containers[0].Ports) != 1 ||
		service.createInput.Containers[0].Ports[0].ContainerPort != 8080 ||
		service.createInput.Affinity == nil ||
		service.createInput.Affinity.NodeAffinity == nil ||
		len(service.createInput.TopologySpreadConstraints) != 1 ||
		service.createInput.Replicas == nil ||
		*service.createInput.Replicas != 3 ||
		!service.createInput.Confirm ||
		service.createInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
	if service.updateInput.ClusterID != clusterID ||
		service.updateInput.Namespace != "model-serving" ||
		service.updateInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.updateInput.Name != "api" ||
		service.updateInput.UID != "deployment-uid" ||
		service.updateInput.ResourceVersion != "12" ||
		len(service.updateInput.Containers) != 1 ||
		service.updateInput.Containers[0].Image != "example/api:v2" ||
		service.updateInput.Replicas == nil ||
		*service.updateInput.Replicas != 5 ||
		!service.updateInput.Confirm ||
		service.updateInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected update input: %+v", service.updateInput)
	}
	if service.cloneInput.ClusterID != clusterID ||
		service.cloneInput.Namespace != "model-serving" ||
		service.cloneInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.cloneInput.SourceName != "api" ||
		service.cloneInput.SourceUID != "deployment-uid" ||
		service.cloneInput.SourceResourceVersion != "12" ||
		service.cloneInput.Name != "api-copy" ||
		!service.cloneInput.Confirm ||
		service.cloneInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected clone input: %+v", service.cloneInput)
	}
	if service.scaleInput.ClusterID != clusterID ||
		service.scaleInput.Namespace != "model-serving" ||
		service.scaleInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.scaleInput.Name != "inference" ||
		service.scaleInput.Replicas != 4 ||
		!service.scaleInput.Confirm ||
		service.scaleInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected scale input: %+v", service.scaleInput)
	}
	if service.restartInput.Resource != kubernetesresource.WorkloadDaemonSets ||
		service.restartInput.Name != "device-plugin" ||
		!service.restartInput.DryRun ||
		service.restartInput.Confirm ||
		service.restartInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected restart input: %+v", service.restartInput)
	}
	if len(service.suspendCalls) != 2 ||
		!service.suspendCalls[0].Suspended ||
		service.suspendCalls[1].Suspended ||
		service.suspendInput.Resource != kubernetesresource.WorkloadCronJobs ||
		service.suspendInput.Name != "cleanup" {
		t.Fatalf("unexpected suspension inputs: %+v", service.suspendCalls)
	}
	if service.deleteInput.Resource != kubernetesresource.WorkloadJobs ||
		service.deleteInput.UID != "job-uid" ||
		service.deleteInput.ResourceVersion != "42" ||
		service.deleteInput.GracePeriodSeconds == nil ||
		*service.deleteInput.GracePeriodSeconds != 30 ||
		service.deleteInput.Propagation !=
			agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND ||
		service.deleteInput.IdempotencyKey != idempotencyKey {
		t.Fatalf("unexpected delete input: %+v", service.deleteInput)
	}
}

func TestKubernetesWorkloadMutationHandlersRejectUnsafeRequests(t *testing.T) {
	t.Parallel()

	const clusterID = "00000000-0000-4000-8000-000000000003"
	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	collectionRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource"
	baseRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource/:workload_name"
	router.POST(collectionRoute, handler.create)
	router.POST(baseRoute+"/clone", handler.clone)
	router.PUT(baseRoute, handler.update)
	router.POST(baseRoute+"/scale", handler.scale)
	router.DELETE(baseRoute, handler.delete)
	baseURL := "/clusters/" + clusterID +
		"/namespaces/model-serving/workloads/deployments/inference"

	testCases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode string
	}{
		{
			name:     "create confirmation required",
			method:   http.MethodPost,
			path:     "/clusters/" + clusterID + "/namespaces/model-serving/workloads/deployments",
			body:     `{"name":"api","containers":[{"name":"main","image":"example/api:v1"}],"confirm":false}`,
			wantCode: "confirmation_required",
		},
		{
			name:     "confirmation required",
			method:   http.MethodPost,
			path:     baseURL + "/scale",
			body:     `{"replicas":4,"dry_run":false,"confirm":false}`,
			wantCode: "confirmation_required",
		},
		{
			name:   "clone confirmation required",
			method: http.MethodPost,
			path:   baseURL + "/clone",
			body: `{"name":"inference-copy","source_uid":"deployment-uid",` +
				`"source_resource_version":"12","dry_run":false,"confirm":false}`,
			wantCode: "confirmation_required",
		},
		{
			name:     "delete UID required",
			method:   http.MethodDelete,
			path:     baseURL,
			body:     `{"dry_run":false,"confirm":true,"uid":""}`,
			wantCode: "invalid_request",
		},
		{
			name:     "query rejected",
			method:   http.MethodPost,
			path:     baseURL + "/scale?force=true",
			body:     `{"replicas":4,"dry_run":true,"confirm":false}`,
			wantCode: "invalid_request",
		},
		{
			name:     "unknown field rejected",
			method:   http.MethodPost,
			path:     baseURL + "/scale",
			body:     `{"replicas":4,"dry_run":true,"confirm":false,"force":true}`,
			wantCode: "invalid_request",
		},
		{
			name:   "unknown advanced scheduling field rejected",
			method: http.MethodPost,
			path:   "/clusters/" + clusterID + "/namespaces/model-serving/workloads/deployments",
			body: `{"name":"api","containers":[{"name":"main","image":"example/api:v1"}],` +
				`"affinity":{"node_affinity":{"required":[],"bypass":true}},"dry_run":true,"confirm":false}`,
			wantCode: "invalid_request",
		},
	}
	for _, testCase := range testCases {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			testCase.method,
			testCase.path,
			bytes.NewBufferString(testCase.body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf(
				"%s status=%d body=%s",
				testCase.name,
				response.Code,
				response.Body,
			)
		}
		assertErrorCode(t, response, testCase.wantCode)
	}
	if service.createInput.ClusterID != "" ||
		service.cloneInput.ClusterID != "" ||
		service.scaleInput.ClusterID != "" ||
		service.deleteInput.ClusterID != "" {
		t.Fatal("unsafe workload mutation reached service")
	}
}

func TestKubernetesWorkloadRevisionHandlersPreserveScopeAndPreconditions(t *testing.T) {
	t.Parallel()

	const (
		clusterID      = "00000000-0000-4000-8000-000000000003"
		idempotencyKey = "0123456789abcdef"
	)
	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	baseRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource/:workload_name"
	router.GET(baseRoute+"/revisions", handler.revisions)
	router.POST(baseRoute+"/rollback", handler.rollback)
	baseURL := "/clusters/" + clusterID + "/namespaces/model-serving/workloads"

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		baseURL+"/deployments/inference/revisions",
		nil,
	))
	if response.Code != http.StatusOK ||
		service.revisionsInput.ClusterID != clusterID ||
		service.revisionsInput.Namespace != "model-serving" ||
		service.revisionsInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.revisionsInput.Name != "inference" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"revisions"`)) {
		t.Fatalf(
			"revisions status=%d input=%+v body=%s",
			response.Code,
			service.revisionsInput,
			response.Body.String(),
		)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		baseURL+"/deployments/inference/rollback",
		bytes.NewBufferString(
			`{"revision":2,"uid":"deployment-uid","resource_version":"12","dry_run":false,"confirm":true}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.rollbackInput.ClusterID != clusterID ||
		service.rollbackInput.Namespace != "model-serving" ||
		service.rollbackInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.rollbackInput.Name != "inference" ||
		service.rollbackInput.Revision != 2 ||
		service.rollbackInput.UID != "deployment-uid" ||
		service.rollbackInput.ResourceVersion != "12" ||
		!service.rollbackInput.Confirm ||
		service.rollbackInput.IdempotencyKey != idempotencyKey {
		t.Fatalf(
			"rollback status=%d input=%+v body=%s",
			response.Code,
			service.rollbackInput,
			response.Body.String(),
		)
	}

	// A rollback that names no version of the object, and one that was never
	// confirmed, are both refused before the service is asked to do anything.
	service.rollbackInput = kubernetesresource.RollbackWorkloadInput{}
	for _, body := range []string{
		`{"revision":2,"resource_version":"12","confirm":true}`,
		`{"revision":2,"uid":"deployment-uid","confirm":true}`,
		`{"revision":2,"uid":"deployment-uid","resource_version":"12","confirm":false}`,
	} {
		response = httptest.NewRecorder()
		request = httptest.NewRequest(
			http.MethodPost,
			baseURL+"/deployments/inference/rollback",
			bytes.NewBufferString(body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("rollback %s status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	if service.rollbackInput.ClusterID != "" {
		t.Fatal("unsafe rollback reached service")
	}
}

func TestKubernetesWorkloadRollbackReportsRevisionFailuresDistinctly(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		err    error
		status int
		code   string
	}{
		{
			kubernetesresource.ErrWorkloadRevisionsUnsupported,
			http.StatusBadRequest,
			"workload_revisions_unsupported",
		},
		{
			kubernetesresource.ErrWorkloadRevisionNotFound,
			http.StatusNotFound,
			"workload_revision_not_found",
		},
		{
			kubernetesresource.ErrWorkloadRevisionUnchanged,
			http.StatusConflict,
			"workload_revision_unchanged",
		},
	}
	for _, testCase := range testCases {
		service := &fakeKubernetesWorkloadService{rollbackError: testCase.err}
		handler := newKubernetesWorkloadHandler(
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			service,
			nil,
			time.Second,
		)
		router := gin.New()
		router.POST(
			"/clusters/:cluster_id/namespaces/:namespace_name/workloads/"+
				":workload_resource/:workload_name/rollback",
			handler.rollback,
		)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/clusters/00000000-0000-4000-8000-000000000003"+
				"/namespaces/model-serving/workloads/deployments/inference/rollback",
			bytes.NewBufferString(
				`{"revision":2,"uid":"deployment-uid","resource_version":"12","confirm":true}`,
			),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
		router.ServeHTTP(response, request)
		if response.Code != testCase.status ||
			!bytes.Contains(response.Body.Bytes(), []byte(`"`+testCase.code+`"`)) {
			t.Fatalf(
				"%v status=%d body=%s",
				testCase.err,
				response.Code,
				response.Body.String(),
			)
		}
	}
}

// Running a CronJob now is a create, not an edit of the CronJob, and the route
// is registered under the create permission for that reason. The handler has to
// hold up its half: only a CronJob, only against the UID the operator saw, only
// with a confirmation, and recorded under an action that says a run was started
// off schedule rather than under a generic object create.
func TestKubernetesCronJobTriggerPinsTheCronJobAndRecordsItsOwnAction(t *testing.T) {
	t.Parallel()

	const (
		clusterID      = "00000000-0000-4000-8000-000000000003"
		idempotencyKey = "0123456789abcdef"
	)
	service := &fakeKubernetesWorkloadService{}
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesWorkloadHandler(
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
	baseRoute := "/clusters/:cluster_id/namespaces/:namespace_name/workloads/" +
		":workload_resource/:workload_name"
	router.POST(baseRoute+"/trigger", handler.trigger)
	baseURL := "/clusters/" + clusterID + "/namespaces/model-serving/workloads"

	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		baseURL+"/cronjobs/nightly-report/trigger",
		bytes.NewBufferString(`{"dry_run":false,"confirm":true,"uid":"cronjob-uid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		service.triggerCallCount != 1 ||
		service.triggerInput.ClusterID != clusterID ||
		service.triggerInput.Namespace != "model-serving" ||
		service.triggerInput.Name != "nightly-report" ||
		service.triggerInput.UID != "cronjob-uid" ||
		!service.triggerInput.Confirm ||
		service.triggerInput.IdempotencyKey != idempotencyKey ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"nightly-report-manual-0a1b2c3d"`)) {
		t.Fatalf("trigger status=%d input=%+v body=%s", response.Code, service.triggerInput, response.Body.String())
	}
	if len(auditStore.events) != 1 ||
		auditStore.events[0].Action != auditaction.KubernetesCronJobTrigger ||
		auditStore.events[0].Result != "succeeded" {
		t.Fatalf("unexpected CronJob trigger audit events: %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost,
		baseURL+"/cronjobs/nightly-report/trigger",
		bytes.NewBufferString(`{"dry_run":true,"confirm":false,"uid":"cronjob-uid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey+"-preview")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!service.triggerInput.DryRun ||
		len(auditStore.events) != 2 ||
		auditStore.events[1].Action != auditaction.KubernetesCronJobTriggerDryRun {
		t.Fatalf("preview status=%d audit=%+v body=%s", response.Code, auditStore.events, response.Body.String())
	}

	service.triggerError = kubernetesresource.ErrCronJobSuspended
	response = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost,
		baseURL+"/cronjobs/nightly-report/trigger",
		bytes.NewBufferString(`{"dry_run":false,"confirm":true,"uid":"cronjob-uid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey+"-suspended")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte("cron_job_suspended")) {
		t.Fatalf("suspended CronJob status=%d body=%s", response.Code, response.Body.String())
	}
	service.triggerError = nil

	triggered := service.triggerCallCount
	for _, testCase := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "not a CronJob",
			path: baseURL + "/deployments/inference/trigger",
			body: `{"dry_run":false,"confirm":true,"uid":"deployment-uid"}`,
		},
		{
			name: "confirmation required",
			path: baseURL + "/cronjobs/nightly-report/trigger",
			body: `{"dry_run":false,"confirm":false,"uid":"cronjob-uid"}`,
		},
		{
			name: "UID required",
			path: baseURL + "/cronjobs/nightly-report/trigger",
			body: `{"dry_run":true,"confirm":false,"uid":""}`,
		},
		{
			name: "query parameters",
			path: baseURL + "/cronjobs/nightly-report/trigger?force=true",
			body: `{"dry_run":true,"confirm":false,"uid":"cronjob-uid"}`,
		},
	} {
		response = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, testCase.path, bytes.NewBufferString(testCase.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", testCase.name, response.Code, response.Body.String())
		}
	}
	if service.triggerCallCount != triggered {
		t.Fatal("unsafe CronJob run reached service")
	}
}
