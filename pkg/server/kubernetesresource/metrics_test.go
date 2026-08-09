package kubernetesresource

import (
	"context"
	"io"
	"net/http"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestServiceListsNodeAndPodMetrics(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_LIST ||
			request.GetResource().GetGroup() != "metrics.k8s.io" ||
			request.GetResource().GetVersion() != "v1beta1" {
			t.Fatalf("unexpected metrics request: cluster=%q request=%+v", clusterID, request)
		}
		var body string
		switch request.GetResource().GetResource() {
		case "nodes":
			if request.GetNamespace() != "" {
				t.Fatalf("NodeMetrics namespace = %q", request.GetNamespace())
			}
			body = `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","items":[{"metadata":{"name":"worker-b"},"timestamp":"2026-08-09T03:04:05Z","window":"30s","usage":{"cpu":"250m","memory":"1Gi"}},{"metadata":{"name":"worker-a"},"timestamp":"2026-08-09T03:04:05Z","window":"30s","usage":{"cpu":"125000000n","memory":"512Mi"}}]}`
		case "pods":
			if request.GetNamespace() != "default" {
				t.Fatalf("PodMetrics namespace = %q", request.GetNamespace())
			}
			body = `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"PodMetricsList","items":[{"metadata":{"name":"api-0"},"timestamp":"2026-08-09T03:04:05Z","window":"20s","containers":[{"name":"app","usage":{"cpu":"100m","memory":"100Mi"}},{"name":"sidecar","usage":{"cpu":"25m","memory":"20Mi"}}]}]}`
		default:
			t.Fatalf("unexpected metrics resource: %q", request.GetResource().GetResource())
		}
		if _, err := io.WriteString(responseBody, body); err != nil {
			t.Fatal(err)
		}
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			ContentType:          kubernetesJSONContentType,
			BodySize:             uint64(len(body)),
		}, nil
	}}
	service := NewService(requester)

	nodes, err := service.ListNodeMetrics(context.Background(), testClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if !nodes.Available || nodes.Reason != "" || len(nodes.Items) != 2 ||
		nodes.Items[0].Name != "worker-a" || nodes.Items[0].CPUUsageMillis != 125 ||
		nodes.Items[0].MemoryUsageBytes != 512*1024*1024 || nodes.Items[0].WindowSeconds != 30 {
		t.Fatalf("NodeMetrics = %+v", nodes)
	}

	pods, err := service.ListPodMetrics(context.Background(), testClusterID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !pods.Available || len(pods.Items) != 1 || pods.Items[0].Name != "api-0" ||
		pods.Items[0].ContainerCount != 2 || pods.Items[0].CPUUsageMillis != 125 ||
		pods.Items[0].MemoryUsageBytes != 120*1024*1024 || pods.Items[0].WindowSeconds != 20 {
		t.Fatalf("PodMetrics = %+v", pods)
	}
}

func TestServiceReportsMissingAndUnavailableMetricsAPI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		result agentv1.ResultCode
		reason string
		want   string
	}{
		{
			name: "not installed", result: agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
			reason: agentResourceNotAllowedReason, want: MetricsUnavailableNotInstalled,
		},
		{
			name: "not ready", result: agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			reason: "ServiceUnavailable", want: MetricsUnavailableNotReady,
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			requester := &fakeResourceRequester{handle: func(
				context.Context,
				string,
				*agentv1.ResourceRequest,
				io.Writer,
			) (*agentv1.ResourceResponse, error) {
				return &agentv1.ResourceResponse{
					Result:               testCase.result,
					KubernetesStatusCode: http.StatusServiceUnavailable,
					Reason:               testCase.reason,
				}, nil
			}}
			result, err := NewService(requester).ListNodeMetrics(context.Background(), testClusterID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Available || result.Reason != testCase.want || result.Message == "" ||
				result.Items == nil {
				t.Fatalf("metrics availability = %+v", result)
			}
		})
	}
}

func TestServiceDoesNotHideKubernetesDiscoveryFailureAsMissingMetrics(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			KubernetesStatusCode: http.StatusServiceUnavailable,
			Reason:               "DiscoveryFailed",
		}, nil
	}}
	if _, err := NewService(requester).ListNodeMetrics(context.Background(), testClusterID); err != ErrClusterUnavailable {
		t.Fatalf("error = %v, want %v", err, ErrClusterUnavailable)
	}
}

func TestServiceDoesNotHideMetricsRBACFailure(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
			KubernetesStatusCode: http.StatusForbidden,
		}, nil
	}}
	if _, err := NewService(requester).ListNodeMetrics(context.Background(), testClusterID); err != ErrClusterAccessDenied {
		t.Fatalf("error = %v, want %v", err, ErrClusterAccessDenied)
	}
}
