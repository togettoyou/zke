package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestServiceListsAndGetsPodsWithinNamespace(t *testing.T) {
	t.Parallel()

	pod := testPod()
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
	if err != nil {
		t.Fatal(err)
	}
	item := unstructured.Unstructured{Object: object}
	remaining := int64(2)
	list := unstructured.UnstructuredList{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "PodList",
			"metadata": map[string]any{
				"continue":           "next-page",
				"resourceVersion":    "43",
				"remainingItemCount": remaining,
			},
		},
		Items: []unstructured.Unstructured{item},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetResource().GetGroup() != "" ||
			request.GetResource().GetVersion() != "v1" ||
			request.GetResource().GetResource() != "pods" ||
			request.GetNamespace() != "model-serving" {
			t.Fatalf("unexpected Pod request: cluster=%q request=%+v", clusterID, request)
		}
		switch request.GetVerb() {
		case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
			options := request.GetListOptions()
			if options.GetLimit() != 25 ||
				options.GetContinueToken() != "current-page" ||
				options.GetLabelSelector() != "app=inference" ||
				options.GetFieldSelector() != "spec.nodeName=worker-a" {
				t.Fatalf("unexpected Pod list options: %+v", options)
			}
			return writeKubernetesObject(t, responseBody, &list), nil
		case agentv1.ResourceVerb_RESOURCE_VERB_GET:
			if request.GetName() != pod.Name {
				t.Fatalf("unexpected Pod name: %q", request.GetName())
			}
			return writeKubernetesObject(t, responseBody, &item), nil
		default:
			t.Fatalf("unexpected Pod verb: %s", request.GetVerb())
			return nil, nil
		}
	}}
	service := NewService(requester)

	page, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID:     testClusterID,
		Namespace:     "model-serving",
		Limit:         25,
		ContinueToken: "current-page",
		LabelSelector: "app=inference",
		FieldSelector: "spec.nodeName=worker-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Pods) != 1 ||
		page.Pods[0].Name != pod.Name ||
		page.Pods[0].NodeName != "worker-a" ||
		page.Pods[0].PodIP != "10.244.1.12" ||
		!page.Pods[0].Ready ||
		page.Pods[0].RestartCount != 4 ||
		page.Pods[0].Controller == nil ||
		page.Pods[0].Controller.Kind != "ReplicaSet" ||
		page.ContinueToken != "next-page" ||
		page.ResourceVersion != "43" ||
		page.RemainingItemCount == nil ||
		*page.RemainingItemCount != 2 {
		t.Fatalf("unexpected Pod page: %+v", page)
	}

	detail, err := service.GetPod(
		context.Background(),
		testClusterID,
		"model-serving",
		pod.Name,
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.UID != "pod-uid" ||
		detail.Annotations["owner"] != "zke" ||
		detail.ServiceAccountName != "inference" ||
		detail.RuntimeClassName != "nvidia" ||
		detail.QOSClass != string(corev1.PodQOSGuaranteed) ||
		len(detail.HostIPs) != 2 ||
		len(detail.PodIPs) != 2 ||
		len(detail.OwnerReferences) != 1 ||
		len(detail.Conditions) != 1 ||
		len(detail.InitContainers) != 1 ||
		len(detail.Containers) != 1 ||
		len(detail.EphemeralContainers) != 1 ||
		detail.EphemeralContainers[0].State.Type != "waiting" ||
		detail.Containers[0].Requests["cpu"] != "500m" ||
		detail.Containers[0].Limits["memory"] != "1Gi" ||
		detail.Containers[0].State.Type != "running" ||
		detail.InitContainers[0].State.Type != "terminated" ||
		detail.InitContainers[0].State.ExitCode == nil ||
		*detail.InitContainers[0].State.ExitCode != 0 {
		t.Fatalf("unexpected Pod detail: %+v", detail)
	}
}

func TestServiceDeletesPodWithSafetyOptions(t *testing.T) {
	t.Parallel()

	const key = "0123456789abcdef"
	gracePeriod := int64(15)
	called := false
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("Pod delete used read-only transport")
			return nil, nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			_ io.Reader,
			_ io.Writer,
			idempotencyKey string,
		) (*agentv1.ResourceResponse, error) {
			called = true
			options := request.GetDeleteOptions()
			preconditions := options.GetPreconditions()
			if clusterID != testClusterID ||
				idempotencyKey != key ||
				request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DELETE ||
				request.GetResource().GetResource() != "pods" ||
				request.GetNamespace() != "model-serving" ||
				request.GetName() != "inference-abcde" ||
				!options.GetDryRun() ||
				options.GetGracePeriodSeconds() != gracePeriod ||
				options.GetPropagation() != agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND ||
				preconditions.GetUid() != "pod-uid" ||
				preconditions.GetResourceVersion() != "42" {
				t.Fatalf("unexpected Pod delete request: cluster=%q key=%q request=%+v", clusterID, idempotencyKey, request)
			}
			return &agentv1.ResourceResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_OK,
				KubernetesStatusCode: 200,
			}, nil
		},
	}
	err := NewService(requester).DeletePod(context.Background(), DeletePodInput{
		ClusterID:          testClusterID,
		Namespace:          "model-serving",
		Name:               "inference-abcde",
		UID:                "pod-uid",
		ResourceVersion:    "42",
		GracePeriodSeconds: &gracePeriod,
		Propagation:        agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND,
		DryRun:             true,
		IdempotencyKey:     key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Pod delete did not reach mutation transport")
	}
}

func TestServiceRejectsInvalidPodScopeAndDeleteBeforeTransport(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid Pod request reached transport")
			return nil, nil
		},
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid Pod delete reached transport")
			return nil, nil
		},
	}
	service := NewService(requester)
	if _, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID: testClusterID,
		Namespace: "INVALID_NAMESPACE",
		Limit:     25,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid Namespace error = %v", err)
	}
	if _, err := service.GetPod(
		context.Background(),
		testClusterID,
		"model-serving",
		"Invalid_Pod",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid Pod name error = %v", err)
	}
	if err := service.DeletePod(context.Background(), DeletePodInput{
		ClusterID:      testClusterID,
		Namespace:      "model-serving",
		Name:           "inference-abcde",
		UID:            "",
		DryRun:         true,
		IdempotencyKey: "0123456789abcdef",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty UID error = %v", err)
	}
}

func testPod() corev1.Pod {
	now := metav1.NewTime(time.Date(2026, time.August, 1, 8, 0, 0, 0, time.UTC))
	started := true
	controller := true
	blockOwnerDeletion := true
	runtimeClass := "nvidia"
	return corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inference-abcde",
			Namespace:         "model-serving",
			UID:               "pod-uid",
			ResourceVersion:   "42",
			CreationTimestamp: now,
			Labels:            map[string]string{"app": "inference"},
			Annotations:       map[string]string{"owner": "zke"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "apps/v1",
				Kind:               "ReplicaSet",
				Name:               "inference-abcde",
				UID:                "replicaset-uid",
				Controller:         &controller,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:           "worker-a",
			ServiceAccountName: "inference",
			SchedulerName:      "default-scheduler",
			PriorityClassName:  "high-priority",
			RuntimeClassName:   &runtimeClass,
			RestartPolicy:      corev1.RestartPolicyAlways,
			DNSPolicy:          corev1.DNSClusterFirst,
			InitContainers: []corev1.Container{{
				Name: "init", Image: "example/init:v1",
			}},
			Containers: []corev1.Container{{
				Name: "main", Image: "example/model:v2", ImagePullPolicy: corev1.PullIfNotPresent,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			}},
			EphemeralContainers: []corev1.EphemeralContainer{{
				EphemeralContainerCommon: corev1.EphemeralContainerCommon{
					Name: "debugger", Image: "example/debug:v1",
				},
				TargetContainerName: "main",
			}},
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			HostIP:            "192.0.2.10",
			HostIPs:           []corev1.HostIP{{IP: "192.0.2.10"}, {IP: "2001:db8::10"}},
			PodIP:             "10.244.1.12",
			PodIPs:            []corev1.PodIP{{IP: "10.244.1.12"}, {IP: "2001:db8:1::12"}},
			StartTime:         &now,
			QOSClass:          corev1.PodQOSGuaranteed,
			NominatedNodeName: "worker-b",
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				ObservedGeneration: 2,
				LastProbeTime:      now,
				LastTransitionTime: now,
			}},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:         "init",
				Ready:        true,
				RestartCount: 1,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 0, StartedAt: now, FinishedAt: now,
				}},
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "main",
				Ready:        true,
				Started:      &started,
				RestartCount: 2,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
					StartedAt: now,
				}},
			}},
			EphemeralContainerStatuses: []corev1.ContainerStatus{{
				Name:         "debugger",
				RestartCount: 1,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ContainerCreating",
				}},
			}},
		},
	}
}
