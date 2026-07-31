package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestWorkloadDetailCoversSupportedKinds(t *testing.T) {
	t.Parallel()

	replicas := int32(2)
	suspend := true
	now := metav1.NewTime(time.Date(2026, time.July, 31, 8, 0, 0, 0, time.UTC))
	metadata := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Name:              name,
			Namespace:         "model-serving",
			UID:               "workload-uid",
			ResourceVersion:   "42",
			Generation:        3,
			CreationTimestamp: now,
			Labels:            map[string]string{"app": name},
			Annotations:       map[string]string{"owner": "zke"},
		}
	}
	template := func() corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name: "init", Image: "example/init:v1",
				}},
				Containers: []corev1.Container{{
					Name: "main", Image: "example/model:v2",
				}},
			},
		}
	}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}}
	completed := metav1.NewTime(now.Add(time.Minute))

	testCases := []struct {
		name       string
		resource   WorkloadResource
		object     runtime.Object
		wantStatus string
		check      func(*testing.T, WorkloadDetail)
	}{
		{
			name:     "Deployment",
			resource: WorkloadDeployments,
			object: &appsv1.Deployment{
				TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
				ObjectMeta: metadata("inference"),
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: selector,
					Template: template(),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 3,
					Replicas:           2,
					UpdatedReplicas:    2,
					ReadyReplicas:      2,
					AvailableReplicas:  2,
				},
			},
			wantStatus: "available",
			check: func(t *testing.T, detail WorkloadDetail) {
				t.Helper()
				if detail.Replicas == nil ||
					detail.Replicas.Desired != 2 ||
					detail.Replicas.Ready != 2 {
					t.Fatalf("unexpected Deployment replicas: %+v", detail.Replicas)
				}
			},
		},
		{
			name:     "StatefulSet",
			resource: WorkloadStatefulSets,
			object: &appsv1.StatefulSet{
				TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
				ObjectMeta: metadata("database"),
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &replicas,
					Selector:    selector,
					Template:    template(),
					ServiceName: "database-headless",
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 3,
					CurrentReplicas:    2,
					ReadyReplicas:      2,
					AvailableReplicas:  2,
					CurrentRevision:    "database-42",
					UpdateRevision:     "database-42",
				},
			},
			wantStatus: "available",
			check: func(t *testing.T, detail WorkloadDetail) {
				t.Helper()
				if detail.ServiceName != "database-headless" {
					t.Fatalf("StatefulSet service name = %q", detail.ServiceName)
				}
			},
		},
		{
			name:     "DaemonSet",
			resource: WorkloadDaemonSets,
			object: &appsv1.DaemonSet{
				TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
				ObjectMeta: metadata("device-plugin"),
				Spec: appsv1.DaemonSetSpec{
					Selector: selector,
					Template: template(),
				},
				Status: appsv1.DaemonSetStatus{
					ObservedGeneration:     3,
					DesiredNumberScheduled: 2,
					CurrentNumberScheduled: 2,
					UpdatedNumberScheduled: 2,
					NumberReady:            2,
					NumberAvailable:        2,
				},
			},
			wantStatus: "available",
			check:      func(*testing.T, WorkloadDetail) {},
		},
		{
			name:     "Job",
			resource: WorkloadJobs,
			object: &batchv1.Job{
				TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
				ObjectMeta: metadata("finetune"),
				Spec: batchv1.JobSpec{
					Parallelism: &replicas,
					Completions: &replicas,
					Template:    template(),
				},
				Status: batchv1.JobStatus{
					Succeeded:      2,
					CompletionTime: &completed,
					Conditions: []batchv1.JobCondition{{
						Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
					}},
				},
			},
			wantStatus: "completed",
			check: func(t *testing.T, detail WorkloadDetail) {
				t.Helper()
				if detail.Job == nil ||
					detail.Job.Succeeded != 2 ||
					detail.Job.CompletionTime == nil {
					t.Fatalf("unexpected Job status: %+v", detail.Job)
				}
			},
		},
		{
			name:     "CronJob",
			resource: WorkloadCronJobs,
			object: &batchv1.CronJob{
				TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
				ObjectMeta: metadata("cleanup"),
				Spec: batchv1.CronJobSpec{
					Schedule: "0 * * * *",
					Suspend:  &suspend,
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{Template: template()},
					},
				},
			},
			wantStatus: "suspended",
			check: func(t *testing.T, detail WorkloadDetail) {
				t.Helper()
				if detail.CronJob == nil ||
					!detail.CronJob.Suspend ||
					detail.CronJob.Schedule != "0 * * * *" {
					t.Fatalf("unexpected CronJob status: %+v", detail.CronJob)
				}
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(testCase.object)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := workloadDetail(
				object,
				testCase.resource,
				"model-serving",
				testCase.object.(metav1.Object).GetName(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Resource != testCase.resource ||
				detail.Status != testCase.wantStatus ||
				detail.Namespace != "model-serving" ||
				len(detail.Images) != 2 ||
				detail.Images[0] != "example/init:v1" ||
				detail.Images[1] != "example/model:v2" ||
				len(detail.Containers) != 1 ||
				len(detail.InitContainers) != 1 ||
				detail.Annotations["owner"] != "zke" {
				t.Fatalf("unexpected workload detail: %+v", detail)
			}
			testCase.check(t, detail)
		})
	}
}

func TestServiceListsAndGetsWorkloadsWithinNamespace(t *testing.T) {
	t.Parallel()

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inference",
			Namespace: "model-serving",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "inference"}},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "main", Image: "example/model:v2",
				}}},
			},
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
	if err != nil {
		t.Fatal(err)
	}
	item := unstructured.Unstructured{Object: object}
	remaining := int64(1)
	list := unstructured.UnstructuredList{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "DeploymentList",
			"metadata": map[string]any{
				"continue":           "next-page",
				"resourceVersion":    "42",
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
			request.GetResource().GetGroup() != "apps" ||
			request.GetResource().GetVersion() != "v1" ||
			request.GetResource().GetResource() != "deployments" ||
			request.GetNamespace() != "model-serving" {
			t.Fatalf("unexpected workload request: cluster=%q request=%+v", clusterID, request)
		}
		switch request.GetVerb() {
		case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
			if request.GetListOptions().GetLimit() != 25 ||
				request.GetListOptions().GetContinueToken() != "current-page" ||
				request.GetListOptions().GetLabelSelector() != "app=inference" {
				t.Fatalf("unexpected workload list options: %+v", request.GetListOptions())
			}
			return writeKubernetesObject(t, responseBody, &list), nil
		case agentv1.ResourceVerb_RESOURCE_VERB_GET:
			if request.GetName() != "inference" {
				t.Fatalf("unexpected workload name: %q", request.GetName())
			}
			return writeKubernetesObject(t, responseBody, &item), nil
		default:
			t.Fatalf("unexpected workload verb: %s", request.GetVerb())
			return nil, nil
		}
	}}
	service := NewService(requester)

	page, err := service.ListWorkloads(context.Background(), ListWorkloadsInput{
		ClusterID:     testClusterID,
		Namespace:     "model-serving",
		Resource:      WorkloadDeployments,
		Limit:         25,
		ContinueToken: "current-page",
		LabelSelector: "app=inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workloads) != 1 ||
		page.Workloads[0].Name != "inference" ||
		page.ContinueToken != "next-page" ||
		page.ResourceVersion != "42" ||
		page.RemainingItemCount == nil ||
		*page.RemainingItemCount != 1 {
		t.Fatalf("unexpected workload page: %+v", page)
	}

	detail, err := service.GetWorkload(
		context.Background(),
		testClusterID,
		"model-serving",
		WorkloadDeployments,
		"inference",
	)
	if err != nil || detail.Name != "inference" {
		t.Fatalf("GetWorkload() detail=%+v err=%v", detail, err)
	}
}

func TestServiceRejectsInvalidWorkloadScopeAndIdentity(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		t.Fatal("invalid workload request reached transport")
		return nil, nil
	}})
	if _, err := service.ListWorkloads(context.Background(), ListWorkloadsInput{
		ClusterID: testClusterID,
		Namespace: "NOT_A_NAMESPACE",
		Resource:  WorkloadDeployments,
		Limit:     25,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid Namespace error = %v", err)
	}
	if _, err := service.GetWorkload(
		context.Background(),
		testClusterID,
		"model-serving",
		WorkloadResource("pods"),
		"demo",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid workload resource error = %v", err)
	}

	wrong := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo", Namespace: "model-serving",
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workloadDetail(
		object,
		WorkloadDeployments,
		"model-serving",
		"demo",
	); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("mismatched workload identity error = %v", err)
	}
}

func TestWorkloadNotFoundUsesGenericResourceError(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			KubernetesStatusCode: http.StatusNotFound,
		}, nil
	}}
	_, err := NewService(requester).GetWorkload(
		context.Background(),
		testClusterID,
		"model-serving",
		WorkloadDeployments,
		"missing",
	)
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("GetWorkload() error = %v, want resource not found", err)
	}
}
