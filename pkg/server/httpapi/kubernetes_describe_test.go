package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type fakeDescribeService struct {
	result       kubernetesdescribe.Result
	err          error
	podInput     kubernetesdescribe.PodInput
	resourceCall kubernetesdescribe.ResourceInput
}

func (service *fakeDescribeService) DescribePod(
	_ context.Context,
	input kubernetesdescribe.PodInput,
) (kubernetesdescribe.Result, error) {
	service.podInput = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeResource(
	_ context.Context,
	input kubernetesdescribe.ResourceInput,
) (kubernetesdescribe.Result, error) {
	service.resourceCall = input
	return service.result, service.err
}

func describeTestRouter(service kubernetesDescribeService) *gin.Engine {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesDescribeHandler(logger, service, nil, time.Second)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/describe",
		handler.pod,
	)
	router.GET(
		"/clusters/:cluster_id/kubernetes/resources/:resource_name/describe",
		handler.resource,
	)
	return router
}

// Describe reads Events, and reading Events is its own permission. A route that
// asked only for cluster.read would hand out a Namespace's Events to callers
// the Event stream refuses, which is the separation cluster.event.read exists
// for.
func TestDescribeRoutesRequireBothTheResourceAndEventPermissions(t *testing.T) {
	t.Parallel()

	wanted := map[string]bool{
		"GET /api/v1/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/describe": false,
		"GET /api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name/describe":       false,
	}
	for _, route := range parseRegisteredRoutes(t) {
		if _, tracked := wanted[route.key()]; !tracked {
			continue
		}
		wanted[route.key()] = true
		// The route table is read as source, so the middleware is matched by the
		// permission constant it names. Rendered middleware spans several lines,
		// so the comparison is made on the text with its layout removed.
		applied := strings.Join(route.middleware, "\n")
		applied = strings.Join(strings.Fields(applied), "")
		for _, permission := range []struct {
			identifier string
			value      rbac.Permission
		}{
			{"rbac.PermissionClusterRead", rbac.PermissionClusterRead},
			{"rbac.PermissionClusterEventRead", rbac.PermissionClusterEventRead},
		} {
			if !strings.Contains(
				applied,
				"RequireCluster("+permission.identifier+",",
			) {
				t.Errorf("%s does not require %s", route.key(), permission.value)
			}
		}
	}
	for key, found := range wanted {
		if !found {
			t.Errorf("describe route %s is no longer registered", key)
		}
	}
}

func TestDescribePodHandlerReturnsTheJoinedView(t *testing.T) {
	t.Parallel()

	exitCode := int32(1)
	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "model-serving",
			Name:       "inference-0",
			UID:        "6f0f6d55-0c0e-4a3f-9a2d-8c6a1f0a9d11",
		},
		Family: kubernetesdescribe.FamilyPod,
		Pod: &kubernetesresource.PodDetail{
			PodSummary: kubernetesresource.PodSummary{Name: "inference-0"},
		},
		Events: kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{{
			UID:    "event-a",
			Reason: "BackOff",
		}}},
		Findings: []kubernetesdescribe.Finding{{
			Code:     kubernetesdescribe.FindingCrashLoopBackOff,
			Severity: kubernetesdescribe.SeverityWarning,
			Scope:    "server",
			ExitCode: &exitCode,
			Evidence: []kubernetesdescribe.Evidence{{
				Kind: kubernetesdescribe.EvidenceContainerState,
				Name: "server",
			}},
		}},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
		nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.podInput.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.podInput.Namespace != "model-serving" ||
		service.podInput.Name != "inference-0" {
		t.Fatalf("unexpected describe input: %+v", service.podInput)
	}
	var body struct {
		Data struct {
			Family   string `json:"family"`
			Findings []struct {
				Code     string `json:"code"`
				ExitCode *int32 `json:"exit_code"`
			} `json:"findings"`
			Events struct {
				Items []struct {
					Reason string `json:"reason"`
				} `json:"items"`
			} `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Family != kubernetesdescribe.FamilyPod ||
		len(body.Data.Findings) != 1 ||
		body.Data.Findings[0].Code != kubernetesdescribe.FindingCrashLoopBackOff ||
		body.Data.Findings[0].ExitCode == nil ||
		*body.Data.Findings[0].ExitCode != 1 ||
		len(body.Data.Events.Items) != 1 {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected cache header: %q", response.Header().Get("Cache-Control"))
	}
}

func TestDescribePodHandlerRefusesQueryParameters(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe?limit=10",
		nil,
	))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.podInput.Name != "" {
		t.Fatal("the Cluster was asked about a request that was refused")
	}
}

func TestDescribeResourceHandlerCarriesTheIdentityQuery(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Family:           kubernetesdescribe.FamilyGeneric,
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/kubernetes/resources/model-cache/describe"+
			"?version=v1&resource=persistentvolumeclaims&namespace=model-serving",
		nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.resourceCall.Resource.Version != "v1" ||
		service.resourceCall.Resource.Resource != "persistentvolumeclaims" ||
		service.resourceCall.Namespace != "model-serving" ||
		service.resourceCall.Name != "model-cache" {
		t.Fatalf("unexpected describe input: %+v", service.resourceCall)
	}
}

func TestDescribeHandlerMapsClusterErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"missing object", kubernetesresource.ErrResourceNotFound, http.StatusNotFound},
		{"agent gone", kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable},
		{"bad target", kubernetesresource.ErrInvalidInput, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			router := describeTestRouter(&fakeDescribeService{err: testCase.err})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
				nil,
			))
			if response.Code != testCase.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDescribeHandlerReportsAnUnconfiguredService(t *testing.T) {
	t.Parallel()

	router := describeTestRouter(nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
		nil,
	))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
