package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testEvictionKey = "0123456789abcdef"

// An eviction must reach the Cluster as the one request shape the Agent accepts
// for it — the pods/{name}/eviction subresource, the flag that opts into it, and
// a UID precondition — and never as a Pod delete wearing a different name.
func TestEvictPodSendsOnlyTheUIDBoundEvictionSubresource(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(context.Context, string, *agentv1.ResourceRequest, io.Writer) (*agentv1.ResourceResponse, error) {
			t.Fatal("eviction used the read-only transport")
			return nil, nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			body io.Reader,
			_ io.Writer,
			key string,
		) (*agentv1.ResourceResponse, error) {
			if clusterID != testClusterID ||
				request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
				request.GetResource().GetGroup() != "" ||
				request.GetResource().GetVersion() != "v1" ||
				request.GetResource().GetResource() != "pods" ||
				request.GetSubresource() != "eviction" ||
				!request.GetPodEvictionAccess() ||
				request.GetSecretAccess() ||
				request.GetNamespace() != "model-serving" ||
				request.GetName() != "inference-abcde" ||
				request.GetMutationOptions().GetDryRun() ||
				key != testEvictionKey {
				t.Fatalf("unexpected eviction request: cluster=%q key=%q request=%+v", clusterID, key, request)
			}
			var eviction map[string]any
			if err := json.NewDecoder(body).Decode(&eviction); err != nil {
				t.Fatal(err)
			}
			uid, _, _ := unstructured.NestedString(eviction, "deleteOptions", "preconditions", "uid")
			// JSON numbers arrive as float64 here, which is why this reads the
			// grace period as one rather than through NestedInt64.
			grace, _, _ := unstructured.NestedFloat64(eviction, "deleteOptions", "gracePeriodSeconds")
			if uid != "pod-uid" || grace != 30 ||
				eviction["apiVersion"] != "policy/v1" || eviction["kind"] != "Eviction" {
				t.Fatalf("unexpected eviction body: %+v", eviction)
			}
			return &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_OK,
				KubernetesStatusCode: http.StatusCreated,
			}, nil
		},
	}
	grace := int64(30)
	result, err := NewService(requester).EvictPod(context.Background(), EvictPodInput{
		ClusterID:          testClusterID,
		Namespace:          "model-serving",
		Name:               "inference-abcde",
		UID:                "pod-uid",
		GracePeriodSeconds: &grace,
		Confirm:            true,
		IdempotencyKey:     testEvictionKey,
	})
	if err != nil || !result.Evicted || result.DryRun ||
		result.Namespace != "model-serving" || result.UID != "pod-uid" {
		t.Fatalf("EvictPod() = %+v, err = %v", result, err)
	}
}

// A preview asks Kubernetes whether the eviction would be allowed. It must carry
// the dry-run marker on both layers — the Agent request and the Eviction's own
// delete options — and must never report the Pod as gone.
func TestEvictPodPreviewMarksBothDryRunLayers(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		mutate: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			body io.Reader,
			_ io.Writer,
			_ string,
		) (*agentv1.ResourceResponse, error) {
			if !request.GetMutationOptions().GetDryRun() {
				t.Fatal("preview did not set the Agent dry-run option")
			}
			var eviction map[string]any
			if err := json.NewDecoder(body).Decode(&eviction); err != nil {
				t.Fatal(err)
			}
			dryRun, _, _ := unstructured.NestedStringSlice(eviction, "deleteOptions", "dryRun")
			if len(dryRun) != 1 || dryRun[0] != "All" {
				t.Fatalf("preview eviction dryRun = %v", dryRun)
			}
			return &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_OK,
				KubernetesStatusCode: http.StatusCreated,
			}, nil
		},
	}
	result, err := NewService(requester).EvictPod(context.Background(), EvictPodInput{
		ClusterID:      testClusterID,
		Namespace:      "model-serving",
		Name:           "inference-abcde",
		UID:            "pod-uid",
		DryRun:         true,
		IdempotencyKey: testEvictionKey,
	})
	if err != nil || result.Evicted || !result.DryRun {
		t.Fatalf("EvictPod() preview = %+v, err = %v", result, err)
	}
}

// 429 from the eviction subresource means a PodDisruptionBudget refused, which
// is not the "retry after reloading" a conflict means and not the throttling a
// capacity error means. It has to survive as its own failure, carrying the API
// Server's account of which budget said no.
func TestEvictPodReportsADisruptionBudgetRefusalAsItsOwnFailure(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			return &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				KubernetesStatusCode: http.StatusTooManyRequests,
				Reason:               "TooManyRequests",
				Message:              "Cannot evict pod as it would violate the pod's disruption budget.",
			}, nil
		},
	}
	_, err := NewService(requester).EvictPod(context.Background(), EvictPodInput{
		ClusterID:      testClusterID,
		Namespace:      "model-serving",
		Name:           "inference-abcde",
		UID:            "pod-uid",
		Confirm:        true,
		IdempotencyKey: testEvictionKey,
	})
	if !errors.Is(err, ErrPodEvictionBlocked) || errors.Is(err, ErrRequestCapacity) {
		t.Fatalf("EvictPod() err = %v", err)
	}
	var blocked *PodEvictionBlocked
	if !errors.As(err, &blocked) || blocked.Detail() == "" {
		t.Fatalf("blocked eviction carried no detail: %v", err)
	}
}

// The guards an eviction shares with a deletion: it names one Pod, by UID, and
// an apply says so explicitly. None of these may reach the Cluster.
func TestEvictPodRefusesRequestsThatNameNoPodOrNoDecision(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid eviction reached the Agent")
			return nil, nil
		},
	}
	service := NewService(requester)
	valid := EvictPodInput{
		ClusterID:      testClusterID,
		Namespace:      "model-serving",
		Name:           "inference-abcde",
		UID:            "pod-uid",
		Confirm:        true,
		IdempotencyKey: testEvictionKey,
	}
	negative := int64(-1)
	for name, mutate := range map[string]func(*EvictPodInput){
		"no cluster":         func(input *EvictPodInput) { input.ClusterID = "not-a-uuid" },
		"no namespace":       func(input *EvictPodInput) { input.Namespace = "" },
		"invalid namespace":  func(input *EvictPodInput) { input.Namespace = "Model_Serving" },
		"no name":            func(input *EvictPodInput) { input.Name = "" },
		"no UID":             func(input *EvictPodInput) { input.UID = "" },
		"no confirmation":    func(input *EvictPodInput) { input.Confirm = false },
		"short key":          func(input *EvictPodInput) { input.IdempotencyKey = "short" },
		"negative grace":     func(input *EvictPodInput) { input.GracePeriodSeconds = &negative },
		"whitespace padding": func(input *EvictPodInput) { input.IdempotencyKey = " " + testEvictionKey },
	} {
		input := valid
		mutate(&input)
		if _, err := service.EvictPod(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: EvictPod() err = %v, want ErrInvalidInput", name, err)
		}
	}
}
