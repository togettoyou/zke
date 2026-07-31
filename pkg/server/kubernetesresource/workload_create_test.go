package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestServiceCreatesTypedWorkloads(t *testing.T) {
	t.Parallel()

	const idempotencyKey = "0123456789abcdef"
	type createCall struct {
		request *agentv1.ResourceRequest
		object  unstructured.Unstructured
	}
	var calls []createCall
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("workload create used read-only transport")
			return nil, nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			requestBody io.Reader,
			responseBody io.Writer,
			key string,
		) (*agentv1.ResourceResponse, error) {
			if clusterID != testClusterID ||
				key != idempotencyKey ||
				request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
				request.GetNamespace() != "model-serving" ||
				request.GetName() != "" {
				t.Fatalf("unexpected workload create request: cluster=%q key=%q request=%+v", clusterID, key, request)
			}
			var object unstructured.Unstructured
			body, err := io.ReadAll(requestBody)
			if err != nil {
				t.Fatal(err)
			}
			if err := object.UnmarshalJSON(body); err != nil {
				t.Fatal(err)
			}
			object.SetUID(types.UID("created-uid"))
			calls = append(calls, createCall{request: request, object: object})
			return writeKubernetesObject(t, responseBody, &object), nil
		},
	}
	service := NewService(requester)
	replicas := int32(3)
	parallelism := int32(2)
	completions := int32(4)
	backoff := int32(5)
	ttl := int32(600)
	suspend := true
	deadline := int64(120)
	successHistory := int32(2)
	failureHistory := int32(1)
	base := CreateWorkloadInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Labels:    map[string]string{"team": "inference"},
		Containers: []WorkloadContainerTemplate{{
			Name: "main", Image: "example/model:v2", ImagePullPolicy: "IfNotPresent",
		}},
		Confirm:        true,
		IdempotencyKey: idempotencyKey,
	}
	testCases := []CreateWorkloadInput{
		func() CreateWorkloadInput {
			input := base
			input.Resource = WorkloadDeployments
			input.Name = "inference"
			input.Replicas = &replicas
			return input
		}(),
		func() CreateWorkloadInput {
			input := base
			input.Resource = WorkloadStatefulSets
			input.Name = "database"
			input.ServiceName = "database-headless"
			input.Replicas = &replicas
			return input
		}(),
		func() CreateWorkloadInput {
			input := base
			input.Resource = WorkloadDaemonSets
			input.Name = "device-plugin"
			input.InitContainers = []WorkloadContainerTemplate{{
				Name: "prepare", Image: "example/init:v1",
			}}
			return input
		}(),
		func() CreateWorkloadInput {
			input := base
			input.Resource = WorkloadJobs
			input.Name = "finetune"
			input.Parallelism = &parallelism
			input.Completions = &completions
			input.BackoffLimit = &backoff
			input.TTLSecondsAfterFinished = &ttl
			return input
		}(),
		func() CreateWorkloadInput {
			input := base
			input.Resource = WorkloadCronJobs
			input.Name = "cleanup"
			input.Parallelism = &parallelism
			input.Completions = &completions
			input.BackoffLimit = &backoff
			input.TTLSecondsAfterFinished = &ttl
			input.Schedule = "0 * * * *"
			input.TimeZone = "Asia/Shanghai"
			input.Suspend = &suspend
			input.ConcurrencyPolicy = "Forbid"
			input.StartingDeadlineSeconds = &deadline
			input.SuccessfulJobsHistoryLimit = &successHistory
			input.FailedJobsHistoryLimit = &failureHistory
			input.DryRun = true
			input.Confirm = false
			return input
		}(),
	}

	for _, input := range testCases {
		detail, err := service.CreateWorkload(context.Background(), input)
		if err != nil {
			t.Fatalf("CreateWorkload(%s) error = %v", input.Resource, err)
		}
		if detail.Resource != input.Resource ||
			detail.Name != input.Name ||
			detail.Namespace != input.Namespace ||
			detail.UID != "created-uid" ||
			len(detail.Containers) != 1 ||
			detail.Containers[0].Image != "example/model:v2" {
			t.Fatalf("CreateWorkload(%s) detail = %+v", input.Resource, detail)
		}
	}

	if len(calls) != len(testCases) {
		t.Fatalf("create calls = %d, want %d", len(calls), len(testCases))
	}
	for index, call := range calls {
		input := testCases[index]
		if call.request.GetResource().GetResource() != string(input.Resource) ||
			call.object.GetName() != input.Name ||
			call.object.GetNamespace() != input.Namespace ||
			call.object.GetLabels()["team"] != "inference" {
			t.Fatalf("create call %d = %+v object=%+v", index, call.request, call.object.Object)
		}
		podLabels, found, err := workloadTemplateLabels(call.object.Object, input.Resource)
		if err != nil || !found ||
			podLabels[workloadSelectorLabel] != workloadSelectorValue(input.Resource, input.Name) ||
			podLabels["team"] != "inference" {
			t.Fatalf("create call %d pod labels = %+v found=%t err=%v", index, podLabels, found, err)
		}
		if index < 3 {
			selector, selectorFound, selectorErr := unstructured.NestedStringMap(
				call.object.Object,
				"spec",
				"selector",
				"matchLabels",
			)
			if selectorErr != nil ||
				!selectorFound ||
				len(selector) != 1 ||
				selector[workloadSelectorLabel] != podLabels[workloadSelectorLabel] {
				t.Fatalf("create call %d selector = %+v found=%t err=%v", index, selector, selectorFound, selectorErr)
			}
		}
	}
	if value, _, _ := unstructured.NestedInt64(calls[0].object.Object, "spec", "replicas"); value != 3 {
		t.Fatalf("Deployment replicas = %d", value)
	}
	if value, _, _ := unstructured.NestedString(calls[1].object.Object, "spec", "serviceName"); value != "database-headless" {
		t.Fatalf("StatefulSet serviceName = %q", value)
	}
	if value, _, _ := unstructured.NestedString(calls[3].object.Object, "spec", "template", "spec", "restartPolicy"); value != "Never" {
		t.Fatalf("Job restartPolicy = %q", value)
	}
	if value, _, _ := unstructured.NestedString(calls[4].object.Object, "spec", "schedule"); value != "0 * * * *" {
		t.Fatalf("CronJob schedule = %q", value)
	}
	if value, _, _ := unstructured.NestedString(calls[4].object.Object, "spec", "timeZone"); value != "Asia/Shanghai" {
		t.Fatalf("CronJob timeZone = %q", value)
	}
	if !calls[4].request.GetMutationOptions().GetDryRun() {
		t.Fatal("CronJob create did not preserve DryRun")
	}
}

func TestServiceRejectsInvalidTypedWorkloadCreates(t *testing.T) {
	t.Parallel()

	called := false
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			return nil, errors.New("unused")
		},
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			called = true
			return nil, nil
		},
	}
	service := NewService(requester)
	base := CreateWorkloadInput{
		ClusterID: testClusterID,
		Namespace: "default",
		Resource:  WorkloadDeployments,
		Name:      "demo",
		Containers: []WorkloadContainerTemplate{{
			Name: "main", Image: "example/app:v1",
		}},
		Confirm:        true,
		IdempotencyKey: "0123456789abcdef",
	}
	replicas := int32(1)
	testCases := []struct {
		name   string
		mutate func(CreateWorkloadInput) CreateWorkloadInput
	}{
		{
			name: "no containers",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Containers = nil
				return input
			},
		},
		{
			name: "duplicate container name",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.InitContainers = []WorkloadContainerTemplate{{Name: "main", Image: "example/init:v1"}}
				return input
			},
		},
		{
			name: "reserved selector label",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Labels = map[string]string{workloadSelectorLabel: ""}
				return input
			},
		},
		{
			name: "Deployment with Job field",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Parallelism = &replicas
				return input
			},
		},
		{
			name: "StatefulSet without Service",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Resource = WorkloadStatefulSets
				return input
			},
		},
		{
			name: "DaemonSet with replicas",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Resource = WorkloadDaemonSets
				input.Replicas = &replicas
				return input
			},
		},
		{
			name: "CronJob without schedule",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Resource = WorkloadCronJobs
				return input
			},
		},
		{
			name: "Job with CronJob field",
			mutate: func(input CreateWorkloadInput) CreateWorkloadInput {
				input.Resource = WorkloadJobs
				input.Schedule = "* * * * *"
				return input
			},
		},
	}
	for _, testCase := range testCases {
		input := testCase.mutate(base)
		if _, err := service.CreateWorkload(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s error = %v, want invalid input", testCase.name, err)
		}
	}
	if called {
		t.Fatal("invalid workload create reached transport")
	}
}

func workloadTemplateLabels(
	object map[string]any,
	resource WorkloadResource,
) (map[string]string, bool, error) {
	path := []string{"spec", "template", "metadata", "labels"}
	if resource == WorkloadCronJobs {
		path = []string{"spec", "jobTemplate", "spec", "template", "metadata", "labels"}
	}
	labels, found, err := unstructured.NestedStringMap(object, path...)
	return labels, found, err
}
