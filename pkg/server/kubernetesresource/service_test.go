package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const testClusterID = "00000000-0000-4000-8000-000000000003"

type fakeResourceRequester struct {
	handle func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error)
	mutate func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Reader,
		io.Writer,
		string,
	) (*agentv1.ResourceResponse, error)
}

func (requester *fakeResourceRequester) RequestResourceMutation(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
	idempotencyKey string,
) (*agentv1.ResourceResponse, error) {
	return requester.mutate(
		ctx,
		clusterID,
		request,
		requestBody,
		responseBody,
		idempotencyKey,
	)
}

func (requester *fakeResourceRequester) RequestResource(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	return requester.handle(ctx, clusterID, request, responseBody)
}

func TestServiceListsNodesWithKubernetesContinuation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	node := testNode("worker-a", now)
	remaining := int64(2)
	list := corev1.NodeList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"},
		ListMeta: metav1.ListMeta{
			ResourceVersion:    "42",
			Continue:           "next-page",
			RemainingItemCount: &remaining,
		},
		Items: []corev1.Node{node},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_LIST ||
			request.GetResource().GetGroup() != "" ||
			request.GetResource().GetVersion() != "v1" ||
			request.GetResource().GetResource() != "nodes" ||
			request.GetRepresentation() !=
				agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT ||
			request.GetListOptions().GetLimit() != 25 ||
			request.GetListOptions().GetContinueToken() != "current-page" ||
			request.GetListOptions().GetLabelSelector() != "pool=gpu" ||
			request.GetListOptions().GetFieldSelector() != "metadata.name=worker-a" {
			t.Fatalf("unexpected Resource request: cluster=%q request=%+v", clusterID, request)
		}
		return writeKubernetesObject(t, responseBody, &list), nil
	}}

	result, err := NewService(requester).ListNodes(
		context.Background(),
		ListNodesInput{
			ClusterID:     testClusterID,
			Limit:         25,
			ContinueToken: "current-page",
			LabelSelector: "pool=gpu",
			FieldSelector: "metadata.name=worker-a",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 ||
		result.Nodes[0].Name != "worker-a" ||
		result.Nodes[0].Status != "ready" ||
		result.Nodes[0].InternalIP != "10.0.0.10" ||
		result.Nodes[0].CPUCapacity != "8" ||
		result.Nodes[0].MemoryAllocatable != "30Gi" ||
		len(result.Nodes[0].Roles) != 1 ||
		result.Nodes[0].Roles[0] != "worker" ||
		result.ContinueToken != "next-page" ||
		result.ResourceVersion != "42" ||
		result.RemainingItemCount == nil ||
		*result.RemainingItemCount != 2 {
		t.Fatalf("unexpected Node page: %+v", result)
	}
}

func TestServiceGetsNodeDetail(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	node := testNode("worker-a", now)
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET ||
			request.GetName() != "worker-a" {
			t.Fatalf("unexpected Resource request: cluster=%q request=%+v", clusterID, request)
		}
		return writeKubernetesObject(t, responseBody, &node), nil
	}}

	result, err := NewService(requester).GetNode(
		context.Background(),
		testClusterID,
		"worker-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "worker-a" ||
		result.ProviderID != "test://worker-a" ||
		result.PodCIDR != "10.244.1.0/24" ||
		result.Architecture != "amd64" ||
		len(result.Addresses) != 2 ||
		len(result.Taints) != 1 ||
		len(result.Conditions) != 1 ||
		result.Labels["pool"] != "gpu" {
		t.Fatalf("unexpected Node detail: %+v", result)
	}
}

func TestServiceMapsTransportAndKubernetesFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		response *agentv1.ResourceResponse
		err      error
		want     error
	}{
		{
			name: "Agent disconnected",
			err:  agentconn.ErrAgentNotConnected,
			want: ErrAgentNotConnected,
		},
		{
			name: "Agent capability missing",
			err:  agentconn.ErrResourceCapabilityMissing,
			want: ErrAgentUnsupported,
		},
		{
			name: "Node absent",
			response: &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
				KubernetesStatusCode: http.StatusNotFound,
			},
			want: ErrNodeNotFound,
		},
		{
			name: "Agent ServiceAccount forbidden",
			response: &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
				KubernetesStatusCode: http.StatusForbidden,
			},
			want: ErrClusterAccessDenied,
		},
		{
			name: "oversized response",
			response: &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				KubernetesStatusCode: http.StatusRequestEntityTooLarge,
			},
			want: ErrResponseTooLarge,
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
				return testCase.response, testCase.err
			}}
			_, err := NewService(requester).GetNode(
				context.Background(),
				testClusterID,
				"worker-a",
			)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("GetNode() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestServiceRejectsInvalidNodeInputsBeforeTransport(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		t.Fatal("invalid input reached Resource transport")
		return nil, nil
	}}
	service := NewService(requester)
	for _, input := range []ListNodesInput{
		{ClusterID: "invalid", Limit: 10},
		{ClusterID: testClusterID, Limit: 0},
		{ClusterID: testClusterID, Limit: MaxNodeListLimit + 1},
		{ClusterID: testClusterID, Limit: 10, LabelSelector: "invalid in ("},
		{ClusterID: testClusterID, Limit: 10, FieldSelector: "metadata.name"},
	} {
		if _, err := service.ListNodes(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ListNodes(%+v) error = %v, want invalid input", input, err)
		}
	}
	if _, err := service.GetNode(
		context.Background(),
		testClusterID,
		"NOT_A_NODE",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetNode() error = %v, want invalid input", err)
	}
}

func writeKubernetesObject(
	t *testing.T,
	writer io.Writer,
	value any,
) *agentv1.ResourceResponse {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: http.StatusOK,
		ContentType:          kubernetesJSONContentType,
		BodySize:             uint64(len(body)),
	}
}

func TestResponseBufferEnforcesInstanceBudget(t *testing.T) {
	t.Parallel()

	service := NewService(nil, Config{MaxBufferedResponseBytes: 4})
	first := service.newResponseBuffer(context.Background())
	defer first.Release()
	if _, err := first.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}

	blockedContext, cancelBlocked := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancelBlocked()
	blocked := service.newResponseBuffer(blockedContext)
	if _, err := blocked.Write([]byte("x")); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("blocked Write error = %v", err)
	}

	first.Release()
	available := service.newResponseBuffer(context.Background())
	defer available.Release()
	if _, err := available.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
}

func testNode(name string, now time.Time) corev1.Node {
	return corev1.Node{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			UID:               types.UID("node-uid"),
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"pool":                           "gpu",
				"node-role.kubernetes.io/worker": "",
			},
			Annotations: map[string]string{"example.com/source": "test"},
		},
		Spec: corev1.NodeSpec{
			ProviderID:    "test://" + name,
			PodCIDR:       "10.244.1.0/24",
			PodCIDRs:      []string{"10.244.1.0/24"},
			Unschedulable: false,
			Taints: []corev1.Taint{{
				Key:    "dedicated",
				Value:  "gpu",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("7500m"),
				corev1.ResourceMemory: resource.MustParse("30Gi"),
				corev1.ResourcePods:   resource.MustParse("100"),
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.10"},
				{Type: corev1.NodeHostName, Address: name},
			},
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				Reason:             "KubeletReady",
				LastHeartbeatTime:  metav1.NewTime(now),
				LastTransitionTime: metav1.NewTime(now),
			}},
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:            "amd64",
				OperatingSystem:         "linux",
				KernelVersion:           "6.8.0",
				OSImage:                 "Test Linux",
				ContainerRuntimeVersion: "containerd://2.0",
				KubeletVersion:          "v1.36.0",
			},
		},
	}
}
