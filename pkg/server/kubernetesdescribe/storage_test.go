package kubernetesdescribe

import (
	"context"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDescribePersistentVolumeClaimExplainsPendingBinding(t *testing.T) {
	t.Parallel()

	claim := kubernetesresource.StorageResourceDetail{
		StorageResourceSummary: kubernetesresource.StorageResourceSummary{
			Resource:        kubernetesresource.StoragePersistentVolumeClaims,
			APIVersion:      "v1",
			Kind:            "PersistentVolumeClaim",
			Namespace:       "models",
			Name:            "weights",
			UID:             "claim-uid",
			ResourceVersion: "41",
			PersistentVolumeClaim: &kubernetesresource.PersistentVolumeClaimSummary{
				Phase: "Pending", RequestedCapacity: "10Gi",
			},
		},
		PersistentVolumeClaimDetail: &kubernetesresource.PersistentVolumeClaimDetail{
			Conditions: []kubernetesresource.PersistentVolumeClaimCondition{},
		},
	}
	access := &fakeResourceAccess{
		claims: map[string]kubernetesresource.StorageResourceDetail{"weights": claim},
	}
	events := &fakeEventSource{events: []corev1.Event{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "weights.provisioning", Namespace: "models", UID: types.UID("event-uid"),
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "PersistentVolumeClaim", Name: "weights", Namespace: "models", UID: types.UID("claim-uid"),
		},
		Type: "Warning", Reason: "ProvisioningFailed", Message: "storage class was not found",
	}}}

	result, err := NewService(access, events, Config{}).DescribePersistentVolumeClaim(
		context.Background(),
		PersistentVolumeClaimInput{
			ClusterID: testClusterID, Namespace: "models", Name: "weights",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Family != FamilyStorage || result.Storage == nil ||
		result.Target.UID != "claim-uid" || result.Target.ResourceVersion != "41" {
		t.Fatalf("unexpected storage projection: %+v", result)
	}
	if events.input.Namespace != "models" || events.input.ResourceUID != "claim-uid" {
		t.Fatalf("Event read was not claim scoped: %+v", events.input)
	}
	if len(result.Findings) != 1 || result.Findings[0].Code != FindingPVCPending ||
		result.Findings[0].Reason != "ProvisioningFailed" ||
		result.Findings[0].Message != "storage class was not found" {
		t.Fatalf("unexpected PVC findings: %+v", result.Findings)
	}
}

func TestDescribePersistentVolumeClaimDoesNotReportABoundClaim(t *testing.T) {
	t.Parallel()

	claim := kubernetesresource.StorageResourceDetail{
		StorageResourceSummary: kubernetesresource.StorageResourceSummary{
			Resource:   kubernetesresource.StoragePersistentVolumeClaims,
			APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "models",
			Name: "weights", UID: "claim-uid",
			PersistentVolumeClaim: &kubernetesresource.PersistentVolumeClaimSummary{Phase: "Bound"},
		},
	}
	access := &fakeResourceAccess{
		claims: map[string]kubernetesresource.StorageResourceDetail{"weights": claim},
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribePersistentVolumeClaim(
		context.Background(),
		PersistentVolumeClaimInput{ClusterID: testClusterID, Namespace: "models", Name: "weights"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("Bound claim reported as unhealthy: %+v", result.Findings)
	}
}
