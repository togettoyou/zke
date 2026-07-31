package kubernetesresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestServiceMutatesTypedWorkloadsWithStableScopedRequests(t *testing.T) {
	t.Parallel()

	const idempotencyKey = "0123456789abcdef"
	type mutationCall struct {
		request *agentv1.ResourceRequest
		body    map[string]any
		key     string
	}
	var calls []mutationCall
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("workload mutation used read-only transport")
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
				request.GetNamespace() != "model-serving" ||
				request.GetName() == "" {
				t.Fatalf("unexpected workload mutation scope: cluster=%q request=%+v", clusterID, request)
			}
			var body map[string]any
			if requestBody != nil {
				if err := json.NewDecoder(requestBody).Decode(&body); err != nil {
					t.Fatal(err)
				}
			}
			calls = append(calls, mutationCall{request: request, body: body, key: key})
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
				return &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_OK,
					KubernetesStatusCode: 200,
				}, nil
			}
			apiVersion := request.GetResource().GetVersion()
			if group := request.GetResource().GetGroup(); group != "" {
				apiVersion = group + "/" + apiVersion
			}
			kind := map[string]string{
				"deployments": "Deployment",
				"daemonsets":  "DaemonSet",
				"cronjobs":    "CronJob",
			}[request.GetResource().GetResource()]
			return writeKubernetesObject(t, responseBody, &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": apiVersion,
					"kind":       kind,
					"metadata": map[string]any{
						"name":      request.GetName(),
						"namespace": request.GetNamespace(),
					},
				},
			}), nil
		},
	}
	service := NewService(requester)
	base := WorkloadMutationInput{
		ClusterID:      testClusterID,
		Namespace:      "model-serving",
		Name:           "inference",
		Confirm:        true,
		IdempotencyKey: idempotencyKey,
	}

	scale := base
	scale.Resource = WorkloadDeployments
	if _, err := service.ScaleWorkload(context.Background(), ScaleWorkloadInput{
		WorkloadMutationInput: scale,
		Replicas:              4,
	}); err != nil {
		t.Fatal(err)
	}
	restart := base
	restart.Resource = WorkloadDaemonSets
	restart.Name = "device-plugin"
	restart.DryRun = true
	restart.Confirm = false
	if _, err := service.RestartWorkload(context.Background(), restart); err != nil {
		t.Fatal(err)
	}
	suspend := base
	suspend.Resource = WorkloadCronJobs
	suspend.Name = "cleanup"
	if _, err := service.SetWorkloadSuspension(
		context.Background(),
		SetWorkloadSuspensionInput{
			WorkloadMutationInput: suspend,
			Suspended:             true,
		},
	); err != nil {
		t.Fatal(err)
	}
	grace := int64(30)
	deletion := base
	deletion.Resource = WorkloadJobs
	deletion.Name = "finetune"
	if err := service.DeleteWorkload(context.Background(), DeleteWorkloadInput{
		WorkloadMutationInput: deletion,
		GracePeriodSeconds:    &grace,
		Propagation:           agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND,
		UID:                   "job-uid",
		ResourceVersion:       "42",
	}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 4 {
		t.Fatalf("mutation calls = %d, want 4", len(calls))
	}
	for index := 0; index < 3; index++ {
		if calls[index].request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_PATCH ||
			calls[index].request.GetPatchType() != agentv1.PatchType_PATCH_TYPE_MERGE ||
			calls[index].key != idempotencyKey {
			t.Fatalf("unexpected patch call %d: %+v", index, calls[index])
		}
	}
	scaleSpec, _ := calls[0].body["spec"].(map[string]any)
	if replicas, _ := scaleSpec["replicas"].(float64); replicas != 4 {
		t.Fatalf("scale patch = %+v", calls[0].body)
	}
	restartValue, _, _ := unstructured.NestedString(
		calls[1].body,
		"spec",
		"template",
		"metadata",
		"annotations",
		workloadRestartAnnotation,
	)
	restartDigest := sha256.Sum256([]byte(idempotencyKey))
	if restartValue != hex.EncodeToString(restartDigest[:]) ||
		!calls[1].request.GetMutationOptions().GetDryRun() {
		t.Fatalf("restart request = %+v annotation = %q", calls[1].request, restartValue)
	}
	if suspended, _, _ := unstructured.NestedBool(calls[2].body, "spec", "suspend"); !suspended {
		t.Fatalf("suspension patch = %+v", calls[2].body)
	}
	deleteRequest := calls[3].request
	if deleteRequest.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DELETE ||
		deleteRequest.GetResource().GetResource() != "jobs" ||
		deleteRequest.GetDeleteOptions().GetPropagation() !=
			agentv1.DeletePropagation_DELETE_PROPAGATION_FOREGROUND ||
		deleteRequest.GetDeleteOptions().GetPreconditions().GetUid() != "job-uid" ||
		deleteRequest.GetDeleteOptions().GetPreconditions().GetResourceVersion() != "42" {
		t.Fatalf("unexpected delete request: %+v", deleteRequest)
	}
}

func TestServiceRejectsUnsupportedTypedWorkloadMutations(t *testing.T) {
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
	base := WorkloadMutationInput{
		ClusterID:      testClusterID,
		Namespace:      "default",
		Resource:       WorkloadJobs,
		Name:           "demo",
		Confirm:        true,
		IdempotencyKey: "0123456789abcdef",
	}
	if _, err := service.ScaleWorkload(context.Background(), ScaleWorkloadInput{
		WorkloadMutationInput: base,
		Replicas:              1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Job scale error = %v", err)
	}
	if _, err := service.RestartWorkload(context.Background(), base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Job restart error = %v", err)
	}
	if _, err := service.SetWorkloadSuspension(
		context.Background(),
		SetWorkloadSuspensionInput{WorkloadMutationInput: base, Suspended: true},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Job suspension error = %v", err)
	}
	if err := service.DeleteWorkload(context.Background(), DeleteWorkloadInput{
		WorkloadMutationInput: base,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("delete without UID error = %v", err)
	}
	if called {
		t.Fatal("unsupported workload mutation reached transport")
	}
}
