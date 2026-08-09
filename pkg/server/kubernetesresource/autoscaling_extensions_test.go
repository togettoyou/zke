package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestVPAInputValidationAndRecommendationParsing(t *testing.T) {
	t.Parallel()

	spec, err := vpaKubernetesSpec(VPASpec{
		Target:     AutoscalingTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
		UpdateMode: "InPlaceOrRecreate",
		ContainerPolicies: []VPAContainerPolicy{{
			ContainerName: "api", MinAllowed: map[string]string{"cpu": "100m"},
			MaxAllowed:          map[string]string{"cpu": "2", "memory": "2Gi"},
			ControlledResources: []string{"cpu", "memory"}, ControlledValues: "RequestsOnly",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	object := extensionObject("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", "default", "api", nil, nil, spec)
	object["status"] = map[string]any{
		"recommendation": map[string]any{"containerRecommendations": []any{map[string]any{
			"containerName": "api", "target": map[string]any{"cpu": "500m", "memory": "1Gi"},
			"lowerBound": map[string]any{"cpu": "100m"}, "upperBound": map[string]any{"cpu": "2"},
		}}},
	}
	detail, err := vpaDetail(object, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if detail.UpdateMode != "InPlaceOrRecreate" || len(detail.ContainerPolicies) != 1 ||
		len(detail.Recommendations) != 1 || detail.Recommendations[0].Target["cpu"] != "500m" {
		t.Fatalf("unexpected VPA detail: %+v", detail)
	}

	_, err = vpaKubernetesSpec(VPASpec{
		Target: AutoscalingTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, UpdateMode: "Auto",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("deprecated Auto mode error = %v, want ErrInvalidInput", err)
	}
}

func TestAutoscalingExtensionDetailsNormalizeOptionalCollections(t *testing.T) {
	t.Parallel()

	vpaObject := extensionObject("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", "default", "api", nil, nil, map[string]any{
		"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"},
		"resourcePolicy": map[string]any{"containerPolicies": []any{map[string]any{
			"containerName": "api",
		}}},
	})
	vpa, err := vpaDetail(vpaObject, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if vpa.ContainerPolicies[0].ControlledResources == nil {
		t.Fatal("unset VPA controlledResources was not normalized to an empty array")
	}

	kedaObject := extensionObject("keda.sh/v1alpha1", "ScaledObject", "default", "worker", nil, nil, map[string]any{
		"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "worker"},
		"triggers":       []any{map[string]any{"type": "cpu", "metadata": map[string]any{"value": "80"}}},
	})
	keda, err := kedaDetail(kedaObject, "default", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if keda.ExternalMetricNames == nil {
		t.Fatal("unset KEDA externalMetricNames was not normalized to an empty array")
	}
}

func TestKEDAResourceMetricTypeIsTopLevel(t *testing.T) {
	t.Parallel()

	spec, err := kedaKubernetesSpec(KEDAScaledObjectSpec{
		Target:          AutoscalingTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "worker"},
		PollingInterval: 30, CooldownPeriod: 300, MaxReplicas: 10,
		Triggers: []KEDATrigger{{Type: "cpu", MetricType: "AverageValue", Metadata: map[string]string{"value": "80m"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	triggers := spec["triggers"].([]any)
	trigger := triggers[0].(map[string]any)
	if trigger["metricType"] != "AverageValue" {
		t.Fatalf("metricType = %v, want AverageValue", trigger["metricType"])
	}
	metadata := trigger["metadata"].(map[string]any)
	if _, exists := metadata["type"]; exists {
		t.Fatalf("deprecated metadata.type was written: %+v", metadata)
	}

	for _, invalid := range []KEDATrigger{
		{Type: "cpu", Metadata: map[string]string{"type": "AverageValue", "value": "80m"}},
		{Type: "memory", MetricType: "Value", Metadata: map[string]string{"value": "128Mi"}},
	} {
		if validKEDATrigger(invalid) {
			t.Fatalf("invalid resource trigger was accepted: %+v", invalid)
		}
	}

	object := extensionObject("keda.sh/v1alpha1", "ScaledObject", "default", "worker", nil, nil, spec)
	detail, err := kedaDetail(object, "default", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Triggers[0].MetricType != "AverageValue" {
		t.Fatalf("parsed metric type = %q", detail.Triggers[0].MetricType)
	}
}

func TestKEDATriggerSecretsAreRejectedAndExistingValuesAreRedacted(t *testing.T) {
	t.Parallel()

	base := KEDAScaledObjectSpec{
		Target:          AutoscalingTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "worker"},
		PollingInterval: 30, CooldownPeriod: 0, MaxReplicas: 20,
		Triggers: []KEDATrigger{{Type: "rabbitmq", Metadata: map[string]string{"queueName": "jobs", "password": "plaintext"}}},
	}
	if _, err := kedaKubernetesSpec(base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inline credential error = %v, want ErrInvalidInput", err)
	}

	object := extensionObject("keda.sh/v1alpha1", "ScaledObject", "default", "worker", nil, nil, map[string]any{
		"scaleTargetRef":  map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "worker"},
		"pollingInterval": int64(30), "cooldownPeriod": int64(0), "maxReplicaCount": int64(20),
		"triggers": []any{map[string]any{"type": "rabbitmq", "metadata": map[string]any{"queueName": "jobs", "password": "legacy-value"}}},
	})
	detail, err := kedaDetail(object, "default", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CooldownPeriod != 0 || detail.Triggers[0].Metadata["password"] != "[redacted]" ||
		len(detail.Triggers[0].RedactedMetadataKeys) != 1 {
		t.Fatalf("unexpected KEDA redaction/defaults: %+v", detail)
	}
}

func TestAutoscalingExtensionListReportsMissingCRDWithoutFailure(t *testing.T) {
	t.Parallel()

	responses := []*agentv1.ResourceResponse{
		{Result: agentv1.ResultCode_RESULT_CODE_FORBIDDEN, KubernetesStatusCode: http.StatusForbidden, Reason: agentResourceNotAllowedReason},
		{Result: agentv1.ResultCode_RESULT_CODE_NOT_FOUND, KubernetesStatusCode: http.StatusNotFound, Reason: string(metav1.StatusReasonNotFound)},
	}
	for index, response := range responses {
		requester := &fakeResourceRequester{handle: func(context.Context, string, *agentv1.ResourceRequest, io.Writer) (*agentv1.ResourceResponse, error) {
			return response, nil
		}}
		page, err := NewService(requester).ListVerticalPodAutoscalers(context.Background(), AutoscalingExtensionListInput{
			ClusterID: testClusterID, Namespace: "default", Limit: 50,
		})
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if page.Available || page.UnavailableReason != "not_installed" || page.Autoscalers == nil {
			t.Fatalf("case %d unexpected missing-CRD page: %+v", index, page)
		}
	}
}

func TestUpdateVPARejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	object := extensionObject("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", "default", "api", nil, nil, map[string]any{
		"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"},
	})
	metadata := object["metadata"].(map[string]any)
	metadata["uid"], metadata["resourceVersion"] = "current-uid", "8"
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, object), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale VPA update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateVerticalPodAutoscaler(context.Background(), UpdateVPAInput{
		ClusterID: testClusterID, Namespace: "default", Name: "api", UID: "stale-uid", ResourceVersion: "8",
		Spec:    VPASpec{Target: AutoscalingTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}},
		Confirm: true, IdempotencyKey: "vpa-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want ErrUpstreamConflict", err)
	}
}

func TestHPAMetricTrendDeduplicatesRapidSamples(t *testing.T) {
	t.Parallel()

	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: types.UID("hpa-uid"), ResourceVersion: "8"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 10,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2, DesiredReplicas: 3},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(hpa)
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeResourceRequester{handle: func(_ context.Context, _ string, _ *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
		return writeKubernetesObject(t, responseBody, object), nil
	}}
	service := NewService(requester)
	first, err := service.GetHorizontalPodAutoscalerMetricTrend(context.Background(), testClusterID, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.GetHorizontalPodAutoscalerMetricTrend(context.Background(), testClusterID, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Points) != 1 || len(second.Points) != 1 || second.WindowSeconds != 3600 {
		t.Fatalf("unexpected trends: first=%+v second=%+v", first, second)
	}
}
