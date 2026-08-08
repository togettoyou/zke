package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

func TestKubernetesNodeHandlerDrain(t *testing.T) {
	t.Parallel()
	service := &fakeKubernetesNodeService{
		drain: func(_ context.Context, input kubernetesresource.DrainNodeInput) (kubernetesresource.DrainNodeResult, error) {
			if input.ClusterID != testHTTPClusterID || input.NodeName != "worker-a" ||
				input.NodeUID != "node-uid" || !input.DryRun || input.Confirm ||
				!input.ForceUnmanaged || input.DeleteEmptyDirData ||
				input.IdempotencyKey != "0123456789abcdef" {
				t.Fatalf("unexpected DrainNode input: %+v", input)
			}
			return kubernetesresource.DrainNodeResult{
				NodeName: input.NodeName, NodeUID: input.NodeUID, DryRun: true,
				Pods: []kubernetesresource.DrainPod{},
			}, nil
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/nodes/worker-a/drain",
		bytes.NewBufferString(`{"uid":"node-uid","dry_run":true,"confirm":false,"force_unmanaged":true,"delete_empty_dir_data":false}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
	response := httptest.NewRecorder()
	nodeHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

type fakeKubernetesNodeService struct {
	list func(
		context.Context,
		kubernetesresource.ListNodesInput,
	) (kubernetesresource.NodePage, error)
	get func(
		context.Context,
		string,
		string,
	) (kubernetesresource.NodeDetail, error)
	drain func(
		context.Context,
		kubernetesresource.DrainNodeInput,
	) (kubernetesresource.DrainNodeResult, error)
}

func (service *fakeKubernetesNodeService) DrainNode(
	ctx context.Context,
	input kubernetesresource.DrainNodeInput,
) (kubernetesresource.DrainNodeResult, error) {
	if service.drain == nil {
		return kubernetesresource.DrainNodeResult{}, errors.New("unexpected Node drain")
	}
	return service.drain(ctx, input)
}

func (service *fakeKubernetesNodeService) ListNodes(
	ctx context.Context,
	input kubernetesresource.ListNodesInput,
) (kubernetesresource.NodePage, error) {
	return service.list(ctx, input)
}

func (service *fakeKubernetesNodeService) GetNode(
	ctx context.Context,
	clusterID string,
	name string,
) (kubernetesresource.NodeDetail, error) {
	return service.get(ctx, clusterID, name)
}

func TestKubernetesNodeHandlerListAndDetail(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	service := &fakeKubernetesNodeService{
		list: func(
			_ context.Context,
			input kubernetesresource.ListNodesInput,
		) (kubernetesresource.NodePage, error) {
			if input.ClusterID != testHTTPClusterID ||
				input.Limit != 25 ||
				input.ContinueToken != "next" ||
				input.LabelSelector != "pool=gpu" ||
				input.FieldSelector != "metadata.name=worker-a" {
				t.Fatalf("unexpected Node list input: %+v", input)
			}
			remaining := int64(1)
			return kubernetesresource.NodePage{
				Nodes: []kubernetesresource.NodeSummary{{
					Name:              "worker-a",
					UID:               "node-uid",
					CreationTimestamp: createdAt,
					Status:            "ready",
					Roles:             []string{"worker"},
					InternalIP:        "10.0.0.10",
					CPUCapacity:       "8",
				}},
				ContinueToken:      "after",
				ResourceVersion:    "42",
				RemainingItemCount: &remaining,
			}, nil
		},
		get: func(
			_ context.Context,
			clusterID string,
			name string,
		) (kubernetesresource.NodeDetail, error) {
			if clusterID != testHTTPClusterID || name != "worker-a" {
				t.Fatalf("unexpected Node detail target: %s/%s", clusterID, name)
			}
			return kubernetesresource.NodeDetail{
				NodeSummary: kubernetesresource.NodeSummary{
					Name:              name,
					UID:               "node-uid",
					CreationTimestamp: createdAt,
					Status:            "ready",
					Roles:             []string{"worker"},
				},
				Labels:       map[string]string{"pool": "gpu"},
				Annotations:  map[string]string{},
				ProviderID:   "test://worker-a",
				PodCIDRs:     []string{"10.244.1.0/24"},
				Addresses:    []kubernetesresource.NodeAddress{},
				Taints:       []kubernetesresource.NodeTaint{},
				Conditions:   []kubernetesresource.NodeCondition{},
				Architecture: "amd64",
			}, nil
		},
	}
	router := nodeHandlerTestRouter(service)

	listResponse := httptest.NewRecorder()
	listRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+
			"/nodes?limit=25&continue=next&label_selector=pool%3Dgpu"+
			"&field_selector=metadata.name%3Dworker-a",
		nil,
	)
	router.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listResponse.Code, listResponse.Body)
	}
	var listData struct {
		Nodes              []nodeSummaryResponse `json:"nodes"`
		ContinueToken      string                `json:"continue_token"`
		ResourceVersion    string                `json:"resource_version"`
		RemainingItemCount *int64                `json:"remaining_item_count"`
	}
	if err := decodeSuccessResponse(listResponse, &listData); err != nil {
		t.Fatal(err)
	}
	if len(listData.Nodes) != 1 ||
		listData.Nodes[0].Name != "worker-a" ||
		listData.ContinueToken != "after" ||
		listData.ResourceVersion != "42" ||
		listData.RemainingItemCount == nil ||
		*listData.RemainingItemCount != 1 {
		t.Fatalf("unexpected Node list response: %+v", listData)
	}
	assertUTC8Time(
		t,
		"Node creation_timestamp",
		listData.Nodes[0].CreationTimestamp,
	)

	detailResponse := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/nodes/worker-a",
		nil,
	)
	router.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detailResponse.Code, detailResponse.Body)
	}
	var detail nodeDetailResponse
	if err := decodeSuccessResponse(detailResponse, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "worker-a" ||
		detail.ProviderID != "test://worker-a" ||
		detail.Labels["pool"] != "gpu" ||
		detail.Architecture != "amd64" {
		t.Fatalf("unexpected Node detail response: %+v", detail)
	}
}

func TestKubernetesNodeHandlerRejectsInvalidQueries(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesNodeService{
		list: func(
			context.Context,
			kubernetesresource.ListNodesInput,
		) (kubernetesresource.NodePage, error) {
			t.Fatal("invalid list query reached service")
			return kubernetesresource.NodePage{}, nil
		},
		get: func(
			context.Context,
			string,
			string,
		) (kubernetesresource.NodeDetail, error) {
			t.Fatal("invalid detail query reached service")
			return kubernetesresource.NodeDetail{}, nil
		},
	}
	router := nodeHandlerTestRouter(service)
	for _, target := range []string{
		"/api/v1/clusters/" + testHTTPClusterID + "/nodes?unknown=value",
		"/api/v1/clusters/" + testHTTPClusterID + "/nodes?limit=abc",
		"/api/v1/clusters/" + testHTTPClusterID + "/nodes?limit=1&limit=2",
		"/api/v1/clusters/" + testHTTPClusterID + "/nodes/worker-a?extra=true",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d: %s", target, response.Code, response.Body)
		}
	}
}

func TestKubernetesNodeHandlerMapsAgentFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		err        error
		status     int
		errorCode  string
		retryAfter string
	}{
		{"not connected", kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", ""},
		{"not found", kubernetesresource.ErrNodeNotFound, http.StatusNotFound, "node_not_found", ""},
		{"forbidden", kubernetesresource.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", ""},
		{"capacity", kubernetesresource.ErrRequestCapacity, http.StatusTooManyRequests, "resource_capacity_exhausted", "1"},
		{"timeout", kubernetesresource.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", ""},
		{"invalid response", kubernetesresource.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", ""},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			service := &fakeKubernetesNodeService{
				list: func(
					context.Context,
					kubernetesresource.ListNodesInput,
				) (kubernetesresource.NodePage, error) {
					return kubernetesresource.NodePage{}, testCase.err
				},
				get: func(
					context.Context,
					string,
					string,
				) (kubernetesresource.NodeDetail, error) {
					return kubernetesresource.NodeDetail{}, errors.New("unused")
				},
			}
			response := httptest.NewRecorder()
			nodeHandlerTestRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(
					http.MethodGet,
					"/api/v1/clusters/"+testHTTPClusterID+"/nodes",
					nil,
				),
			)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.status, response.Body)
			}
			assertErrorCode(t, response, testCase.errorCode)
			if response.Header().Get("Retry-After") != testCase.retryAfter {
				t.Fatalf(
					"Retry-After = %q, want %q",
					response.Header().Get("Retry-After"),
					testCase.retryAfter,
				)
			}
		})
	}
}

const testHTTPClusterID = "00000000-0000-4000-8000-000000000003"

func nodeHandlerTestRouter(service kubernetesNodeService) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	handler := newKubernetesNodeHandler(
		discardLogger(),
		service,
		nil,
		5*time.Second,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/nodes",
		handler.list,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/nodes/:node_name",
		handler.get,
	)
	router.POST(
		"/api/v1/clusters/:cluster_id/nodes/:node_name/drain",
		handler.drain,
	)
	return router
}
