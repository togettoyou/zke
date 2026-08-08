package kubernetesresource

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestEvictDrainPodUsesOnlyTheDedicatedUIDBoundSubresource(t *testing.T) {
	t.Parallel()
	requester := &fakeResourceRequester{
		handle: func(context.Context, string, *agentv1.ResourceRequest, io.Writer) (*agentv1.ResourceResponse, error) {
			t.Fatal("eviction used read-only transport")
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
			if clusterID != testClusterID || request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
				request.GetResource().GetResource() != "pods" || request.GetSubresource() != "eviction" ||
				!request.GetPodEvictionAccess() || request.GetNamespace() != "default" ||
				request.GetName() != "api" || len(key) != 64 {
				t.Fatalf("unexpected eviction request: cluster=%q key=%q request=%+v", clusterID, key, request)
			}
			var eviction map[string]any
			if err := json.NewDecoder(body).Decode(&eviction); err != nil {
				t.Fatal(err)
			}
			uid, _, _ := unstructured.NestedString(eviction, "deleteOptions", "preconditions", "uid")
			if uid != "pod-uid" {
				t.Fatalf("eviction UID precondition = %q", uid)
			}
			return &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				KubernetesStatusCode: http.StatusTooManyRequests,
				Reason:               "TooManyRequests",
			}, nil
		},
	}
	service := NewService(requester)
	response, err := service.evictDrainPod(context.Background(), DrainNodeInput{
		ClusterID: testClusterID, DryRun: true, IdempotencyKey: "0123456789abcdef",
	}, DrainPod{Namespace: "default", Name: "api", UID: "pod-uid"})
	if err != nil || response.GetKubernetesStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("evictDrainPod() response=%+v err=%v", response, err)
	}
}

func TestClassifyDrainPodKeepsUnsafePodsOutByDefault(t *testing.T) {
	t.Parallel()
	controller := true
	tests := []struct {
		name         string
		pod          corev1.Pod
		wantDecision DrainPodDecision
		wantReason   string
	}{
		{
			name:         "unmanaged",
			pod:          drainTestPod("standalone"),
			wantDecision: DrainPodBlock,
			wantReason:   "UnmanagedPod",
		},
		{
			name: "empty dir",
			pod: func() corev1.Pod {
				pod := drainTestPod("api")
				pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Controller: &controller}}
				pod.Spec.Volumes = []corev1.Volume{{VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
				return pod
			}(),
			wantDecision: DrainPodBlock,
			wantReason:   "EmptyDirData",
		},
		{
			name: "daemonset",
			pod: func() corev1.Pod {
				pod := drainTestPod("agent")
				pod.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Controller: &controller}}
				return pod
			}(),
			wantDecision: DrainPodSkip,
			wantReason:   "DaemonSetPod",
		},
		{
			name: "mirror",
			pod: func() corev1.Pod {
				pod := drainTestPod("apiserver")
				pod.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "mirror-hash"}
				return pod
			}(),
			wantDecision: DrainPodSkip,
			wantReason:   "MirrorPod",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := classifyDrainPod(&testCase.pod, DrainNodeInput{})
			if result.Decision != testCase.wantDecision || result.Reason != testCase.wantReason {
				t.Fatalf("classifyDrainPod() = %+v", result)
			}
		})
	}
}

func TestClassifyDrainPodRequiresBothExplicitDataLossChoices(t *testing.T) {
	t.Parallel()
	pod := drainTestPod("standalone")
	pod.Spec.Volumes = []corev1.Volume{{VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	result := classifyDrainPod(&pod, DrainNodeInput{ForceUnmanaged: true})
	if result.Reason != "EmptyDirData" {
		t.Fatalf("force unmanaged also accepted emptyDir data loss: %+v", result)
	}
	result = classifyDrainPod(&pod, DrainNodeInput{ForceUnmanaged: true, DeleteEmptyDirData: true})
	if result.Decision != DrainPodEvict {
		t.Fatalf("both explicit choices did not make Pod evictable: %+v", result)
	}
}

func drainTestPod(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec:       corev1.PodSpec{NodeName: "worker-a"},
	}
}
