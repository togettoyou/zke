package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestCreateHorizontalPodAutoscalerObjectCoversMetricsAndBehavior(t *testing.T) {
	t.Parallel()

	minReplicas := int32(2)
	utilization := int32(70)
	window := int32(300)
	object, err := createHorizontalPodAutoscalerObject(CreateHorizontalPodAutoscalerInput{
		Namespace: "default", Name: "api",
		Spec: HorizontalPodAutoscalerSpec{
			Target:      HPAScaleTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
			MinReplicas: &minReplicas, MaxReplicas: 10,
			Metrics: []HPAMetricSpec{
				{Type: "Resource", Resource: &HPAResourceMetricSpec{Name: "cpu", Target: HPAMetricTarget{Type: "Utilization", AverageUtilization: &utilization}}},
				{Type: "ContainerResource", ContainerResource: &HPAResourceMetricSpec{Name: "memory", Container: "api", Target: HPAMetricTarget{Type: "AverageValue", AverageValue: "512Mi"}}},
			},
			Behavior: &HPABehavior{ScaleDown: &HPAScalingRules{
				StabilizationWindowSeconds: &window, SelectPolicy: "Min",
				Policies: []HPAScalingPolicy{{Type: "Percent", Value: 25, PeriodSeconds: 60}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &hpa) != nil ||
		hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 ||
		len(hpa.Spec.Metrics) != 2 || hpa.Spec.Metrics[0].Resource.Target.AverageUtilization == nil ||
		*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 70 ||
		hpa.Spec.Metrics[1].ContainerResource.Target.AverageValue == nil ||
		hpa.Spec.Metrics[1].ContainerResource.Target.AverageValue.Cmp(resource.MustParse("512Mi")) != 0 ||
		hpa.Spec.Behavior == nil || hpa.Spec.Behavior.ScaleDown == nil || len(hpa.Spec.Behavior.ScaleDown.Policies) != 1 {
		t.Fatalf("unexpected HPA: %+v", hpa)
	}
}

func TestHorizontalPodAutoscalerDetailIncludesUnsupportedMetricViews(t *testing.T) {
	t.Parallel()

	average := resource.MustParse("10")
	observedGeneration := int64(4)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: types.UID("hpa-uid"), ResourceVersion: "8", Generation: 4},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
			MaxReplicas:    10,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ExternalMetricSourceType, External: &autoscalingv2.ExternalMetricSource{
				Metric: autoscalingv2.MetricIdentifier{Name: "queue_depth"},
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: &average},
			}}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			ObservedGeneration: &observedGeneration, CurrentReplicas: 3, DesiredReplicas: 5,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue}},
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(hpa)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := horizontalPodAutoscalerDetail(object, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Generation != 4 || detail.ObservedGeneration == nil || *detail.ObservedGeneration != 4 ||
		detail.MinReplicas != 1 || detail.CurrentReplicas != 3 || detail.DesiredReplicas != 5 || !detail.ScalingActive ||
		len(detail.Metrics) != 1 || detail.Metrics[0].Type != "External" || detail.Metrics[0].Metric == nil ||
		detail.Metrics[0].Metric.Name != "queue_depth" || detail.Metrics[0].Target.AverageValue != "10" ||
		len(detail.CurrentMetrics) != 0 || len(detail.Conditions) != 1 {
		t.Fatalf("unexpected HPA detail: %+v", detail)
	}
}

func TestHorizontalPodAutoscalerInputValidation(t *testing.T) {
	t.Parallel()

	utilization := int32(70)
	tests := []HorizontalPodAutoscalerSpec{
		{Target: HPAScaleTarget{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "api"}, MaxReplicas: 10},
		{Target: HPAScaleTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 0},
		{
			Target: HPAScaleTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 10,
			Metrics: []HPAMetricSpec{{Type: "Resource", Resource: &HPAResourceMetricSpec{Name: "cpu", Container: "api", Target: HPAMetricTarget{Type: "Utilization", AverageUtilization: &utilization}}}},
		},
		{
			Target: HPAScaleTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 10,
			Behavior: &HPABehavior{ScaleDown: &HPAScalingRules{Policies: []HPAScalingPolicy{{Type: "Percent", Value: 0, PeriodSeconds: 60}}}},
		},
	}
	for index, input := range tests {
		if _, err := horizontalPodAutoscalerKubernetesSpec(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d error = %v, want ErrInvalidInput", index, err)
		}
	}
}

func TestUpdateHorizontalPodAutoscalerRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: types.UID("current-uid"), ResourceVersion: "8"},
	}
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, hpa), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale HPA update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateHorizontalPodAutoscaler(context.Background(), UpdateHorizontalPodAutoscalerInput{
		ClusterID: testClusterID, Namespace: "default", Name: "api", UID: "stale-uid", ResourceVersion: "8",
		Spec:    HorizontalPodAutoscalerSpec{Target: HPAScaleTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, MaxReplicas: 10},
		Confirm: true, IdempotencyKey: "hpa-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want ErrUpstreamConflict", err)
	}
}
